// Copyright 2019 The Go Cloud Development Kit Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mongodocstore

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"gocloud.dev/internal/docstore/driver"
)

// Encode and decode to map[string]interface{}.
// This isn't ideal, because the mongo client encodes/decodes a second time.
// TODO(jba): find a way do only one encode/decode.

// This code is copied from memdocstore/codec.go, with some changes:
// - special treatment for primitive.Binary

func encodeDoc(doc driver.Document) (map[string]interface{}, error) {
	var e encoder
	if err := doc.Encode(&e); err != nil {
		return nil, err
	}
	return e.val.(map[string]interface{}), nil
}

func encodeValue(x interface{}) (interface{}, error) {
	var e encoder
	if err := driver.Encode(reflect.ValueOf(x), &e); err != nil {
		return nil, err
	}
	return e.val, nil
}

type encoder struct {
	val interface{}
}

func (e *encoder) EncodeNil()                 { e.val = nil }
func (e *encoder) EncodeBool(x bool)          { e.val = x }
func (e *encoder) EncodeInt(x int64)          { e.val = x }
func (e *encoder) EncodeUint(x uint64)        { e.val = int64(x) }
func (e *encoder) EncodeBytes(x []byte)       { e.val = x }
func (e *encoder) EncodeFloat(x float64)      { e.val = x }
func (e *encoder) EncodeComplex(x complex128) { e.val = []float64{real(x), imag(x)} }
func (e *encoder) EncodeString(x string)      { e.val = x }
func (e *encoder) ListIndex(int)              { panic("impossible") }
func (e *encoder) MapKey(string)              { panic("impossible") }

var typeOfGoTime = reflect.TypeOf(time.Time{})

func (e *encoder) EncodeSpecial(v reflect.Value) (bool, error) {
	// Treat time "specially" as itself (otherwise its BinaryMarshal method will be called).
	if v.Type() == typeOfGoTime {
		e.val = v.Interface()
		return true, nil
	}
	return false, nil
}

func (e *encoder) EncodeList(n int) driver.Encoder {
	// All slices and arrays are encoded as []interface{}
	s := make([]interface{}, n)
	e.val = s
	return &listEncoder{s: s}
}

type listEncoder struct {
	s []interface{}
	encoder
}

func (e *listEncoder) ListIndex(i int) { e.s[i] = e.val }

type mapEncoder struct {
	m        map[string]interface{}
	isStruct bool
	encoder
}

func (e *encoder) EncodeMap(n int, isStruct bool) driver.Encoder {
	m := make(map[string]interface{}, n)
	e.val = m
	return &mapEncoder{m: m, isStruct: isStruct}
}

func (e *mapEncoder) MapKey(k string) {
	// The BSON codec encodes structs by  lower-casing field names.
	if e.isStruct {
		k = strings.ToLower(k)
	}
	e.m[k] = e.val
}

////////////////////////////////////////////////////////////////

// decodeDoc decodes m into ddoc.
func decodeDoc(m map[string]interface{}, ddoc driver.Document) error {
	return ddoc.Decode(decoder{m})
}

type decoder struct {
	val interface{}
}

func (d decoder) String() string {
	return fmt.Sprint(d.val)
}

func (d decoder) AsNull() bool {
	return d.val == nil
}

func (d decoder) AsBool() (bool, bool) {
	b, ok := d.val.(bool)
	return b, ok
}

func (d decoder) AsString() (string, bool) {
	s, ok := d.val.(string)
	return s, ok
}

func (d decoder) AsInt() (int64, bool) {
	switch v := d.val.(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	default:
		return 0, false
	}
}

func (d decoder) AsUint() (uint64, bool) {
	i, ok := d.val.(int64)
	return uint64(i), ok
}

func (d decoder) AsFloat() (float64, bool) {
	f, ok := d.val.(float64)
	return f, ok
}

func (d decoder) AsComplex() (complex128, bool) {
	switch v := d.val.(type) {
	case []float64:
		if len(v) != 2 {
			return 0, false
		}
		return complex(v[0], v[1]), true
	case primitive.A: // []interface{}
		if len(v) != 2 {
			return 0, false
		}
		r, ok := v[0].(float64)
		if !ok {
			return 0, false
		}
		i, ok := v[1].(float64)
		if !ok {
			return 0, false
		}
		return complex(r, i), true
	default:
		return 0, false
	}
}

func (d decoder) AsBytes() ([]byte, bool) {
	switch v := d.val.(type) {
	case []byte:
		return v, true
	case primitive.Binary:
		return v.Data, true
	default:
		return nil, false
	}
}

func (d decoder) AsInterface() (interface{}, error) {
	return toGoValue(d.val)
}

func toGoValue(v interface{}) (interface{}, error) {
	switch v := v.(type) {
	case primitive.A:
		r := make([]interface{}, len(v))
		for i, e := range v {
			d, err := toGoValue(e)
			if err != nil {
				return nil, err
			}
			r[i] = d
		}
		return r, nil
	case primitive.Binary:
		return v.Data, nil
	case primitive.DateTime:
		return bsonDateTimeToTime(v), nil
	case map[string]interface{}:
		r := map[string]interface{}{}
		for k, e := range v {
			d, err := toGoValue(e)
			if err != nil {
				return nil, err
			}
			r[k] = d
		}
		return r, nil
	default:
		return v, nil
	}
}

func (d decoder) ListLen() (int, bool) {
	if s, ok := d.val.(primitive.A); ok {
		return len(s), true
	}
	return 0, false
}

func (d decoder) DecodeList(f func(i int, d2 driver.Decoder) bool) {
	for i, e := range d.val.(primitive.A) {
		if !f(i, decoder{e}) {
			return
		}
	}
}

func (d decoder) MapLen() (int, bool) {
	if m, ok := d.val.(map[string]interface{}); ok {
		return len(m), true
	}
	return 0, false
}

func (d decoder) DecodeMap(f func(key string, d2 driver.Decoder) bool) {
	for k, v := range d.val.(map[string]interface{}) {
		if !f(k, decoder{v}) {
			return
		}
	}
}

func (d decoder) AsSpecial(v reflect.Value) (bool, interface{}, error) {
	switch v := d.val.(type) {
	case primitive.Binary:
		return true, v.Data, nil
	case primitive.DateTime:
		// A DateTime represents milliseconds since the Unix epoch.
		return true, bsonDateTimeToTime(v), nil
	default:
		return false, nil, nil
	}
}

func bsonDateTimeToTime(dt primitive.DateTime) time.Time {
	return time.Unix(int64(dt)/1000, int64(dt)%1000*1e6)
}
