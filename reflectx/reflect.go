// Package reflectx implements extensions to the standard reflect lib suitable
// for implementing marshaling and unmarshaling packages.  The main Mapper type
// allows for Go-compatible named atribute access, including accessing embedded
// struct attributes and the ability to use  functions and struct tags to
// customize field names.
//
package reflectx

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
)

type fieldInfo struct {
	Index    []int
	Path     string
	Field    reflect.StructField
	Zero     reflect.Value
	Name     string
	Options  map[string]string
	Embedded bool
}

type fields []fieldInfo

func (f fields) GetByPath(path string) (fieldInfo, bool) {
	for _, fi := range f {
		if fi.Path == path {
			return fi, true
		}
	}
	return fieldInfo{}, false
}

func (f fields) GetByTraversal(index []int) (fieldInfo, bool) {
	n := len(index)
	for _, fi := range f {
		if len(fi.Index) != n {
			continue
		}
		same := true
		for i, v := range fi.Index {
			if index[i] != v {
				same = false
				break
			}
		}
		if same {
			return fi, true
		}
	}
	return fieldInfo{}, false
}

func (f fields) FieldMap() map[string]fieldInfo {
	fm := map[string]fieldInfo{}
	for _, fi := range f {
		if fi.Name != "" && !fi.Embedded {
			fm[fi.Path] = fi
		}
	}
	return fm
}

// Mapper is a general purpose mapper of names to struct fields.  A Mapper
// behaves like most marshallers, optionally obeying a field tag for name
// mapping and a function to provide a basic mapping of fields to names.
type Mapper struct {
	cache      map[reflect.Type]fields
	tagName    string
	tagMapFunc func(string) string
	mapFunc    func(string) string
	mutex      sync.Mutex
}

// NewMapper returns a new mapper which optionally obeys the field tag given
// by tagName.  If tagName is the empty string, it is ignored.
func NewMapper(tagName string) *Mapper {
	return &Mapper{
		cache:   make(map[reflect.Type]fields),
		tagName: tagName,
	}
}

// NewMapperTagFunc returns a new mapper which contains a mapper for field names
// AND a mapper for tag values.  This is useful for tags like json which can
// have values like "name,omitempty".
func NewMapperTagFunc(tagName string, mapFunc, tagMapFunc func(string) string) *Mapper {
	return &Mapper{
		cache:      make(map[reflect.Type]fields),
		tagName:    tagName,
		mapFunc:    mapFunc,
		tagMapFunc: tagMapFunc,
	}
}

// NewMapperFunc returns a new mapper which optionally obeys a field tag and
// a struct field name mapper func given by f.  Tags will take precedence, but
// for any other field, the mapped name will be f(field.Name)
func NewMapperFunc(tagName string, f func(string) string) *Mapper {
	return &Mapper{
		cache:   make(map[reflect.Type]fields),
		tagName: tagName,
		mapFunc: f,
	}
}

// TypeMap returns a mapping of field strings to int slices representing
// the traversal down the struct to reach the field.
func (m *Mapper) TypeMap(t reflect.Type) fields {
	m.mutex.Lock()
	mapping, ok := m.cache[t]
	if !ok {
		mapping = getMapping(t, m.tagName, m.mapFunc, m.tagMapFunc)
		m.cache[t] = mapping
	}
	m.mutex.Unlock()
	return mapping
}

// FieldMap returns the mapper's mapping of field names to reflect values.  Panics
// if v's Kind is not Struct, or v is not Indirectable to a struct kind.
// func (m *Mapper) FieldMap(v reflect.Value) map[string]reflect.Value {
// 	v = reflect.Indirect(v)
// 	mustBe(v, reflect.Struct)

// 	r := map[string]reflect.Value{}
// 	nm := m.TypeMap(v.Type())
// 	for tagName, fi := range nm {
// 		r[tagName] = FieldByIndexes(v, fi.Index)
// 	}
// 	return r
// }

// FieldByName returns a field by the its mapped name as a reflect.Value.
// Panics if v's Kind is not Struct or v is not Indirectable to a struct Kind.
// Returns zero Value if the name is not found.
func (m *Mapper) FieldByName(v reflect.Value, name string) reflect.Value {
	v = reflect.Indirect(v)
	mustBe(v, reflect.Struct)

	tm := m.TypeMap(v.Type())
	fi, ok := tm.GetByPath(name)
	if !ok {
		return v
	}
	return FieldByIndexes(v, fi.Index)
}

// FieldsByName returns a slice of values corresponding to the slice of names
// for the value.  Panics if v's Kind is not Struct or v is not Indirectable
// to a struct Kind.  Returns zero Value for each name not found.
func (m *Mapper) FieldsByName(v reflect.Value, names []string) []reflect.Value {
	v = reflect.Indirect(v)
	mustBe(v, reflect.Struct)

	tm := m.TypeMap(v.Type())
	vals := make([]reflect.Value, 0, len(names))
	for _, name := range names {
		fi, ok := tm.GetByPath(name)
		if !ok {
			vals = append(vals, *new(reflect.Value))
		} else {
			vals = append(vals, FieldByIndexes(v, fi.Index))
		}
	}
	return vals
}

// TraversalsByName returns a slice of int slices which represent the struct
// traversals for each mapped name.  Panics if t is not a struct or Indirectable
// to a struct.  Returns empty int slice for each name not found.
func (m *Mapper) TraversalsByName(t reflect.Type, names []string) [][]int {
	t = Deref(t)
	mustBe(t, reflect.Struct)
	tm := m.TypeMap(t)

	r := make([][]int, 0, len(names))
	for _, name := range names {
		fi, ok := tm.GetByPath(name)
		if !ok {
			r = append(r, []int{})
		} else {
			r = append(r, fi.Index)
		}
	}
	return r
}

// FieldByIndexes returns a value for a particular struct traversal.
func FieldByIndexes(v reflect.Value, indexes []int) reflect.Value {
	for _, i := range indexes {
		v = reflect.Indirect(v).Field(i)
		// if this is a pointer, it's possible it is nil
		if v.Kind() == reflect.Ptr && v.IsNil() {
			alloc := reflect.New(Deref(v.Type()))
			v.Set(alloc)
		}
		if v.Kind() == reflect.Map && v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
	}
	return v
}

// FieldByIndexesReadOnly returns a value for a particular struct traversal,
// but is not concerned with allocating nil pointers because the value is
// going to be used for reading and not setting.
func FieldByIndexesReadOnly(v reflect.Value, indexes []int) reflect.Value {
	for _, i := range indexes {
		v = reflect.Indirect(v).Field(i)
	}
	return v
}

// Deref is Indirect for reflect.Types
func Deref(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// -- helpers & utilities --

type kinder interface {
	Kind() reflect.Kind
}

// mustBe checks a value against a kind, panicing with a reflect.ValueError
// if the kind isn't that which is required.
func mustBe(v kinder, expected reflect.Kind) {
	k := v.Kind()
	if k != expected {
		panic(&reflect.ValueError{Method: methodName(), Kind: k})
	}
}

// methodName is returns the caller of the function calling methodName
func methodName() string {
	pc, _, _, _ := runtime.Caller(2)
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "unknown method"
	}
	return f.Name()
}

type typeQueue struct {
	t  reflect.Type
	fi fieldInfo
	pp string // Parent path
}

// A copying append that creates a new slice each time.
func apnd(is []int, i int) []int {
	x := make([]int, len(is)+1)
	for p, n := range is {
		x[p] = n
	}
	x[len(x)-1] = i
	return x
}

// getMapping returns a mapping for the t type, using the tagName, mapFunc and
// tagMapFunc to determine the canonical names of fields.
func getMapping(t reflect.Type, tagName string, mapFunc, tagMapFunc func(string) string) fields {
	m := []fieldInfo{}

	queue := []typeQueue{}
	queue = append(queue, typeQueue{Deref(t), fieldInfo{}, ""})

	for len(queue) != 0 {
		// pop the first item off of the queue
		tq := queue[0]
		queue = queue[1:]

		// iterate through all of its fields
		for fieldPos := 0; fieldPos < tq.t.NumField(); fieldPos++ {
			f := tq.t.Field(fieldPos)

			fi := fieldInfo{}
			fi.Field = f
			fi.Zero = reflect.New(f.Type).Elem()
			fi.Options = map[string]string{}

			var tag, name string
			if tagName != "" && strings.Contains(string(f.Tag), tagName+":") {
				tag = f.Tag.Get(tagName)
				name = tag
			} else {
				if mapFunc != nil {
					name = mapFunc(f.Name)
				}
			}

			parts := strings.Split(name, ",")
			if len(parts) > 1 {
				name = parts[0]
				for _, opt := range parts[1:] {
					kv := strings.Split(opt, "=")
					if len(kv) > 1 {
						fi.Options[kv[0]] = kv[1]
					} else {
						fi.Options[kv[0]] = ""
					}
				}
			}

			// TODO: what to do with this...?
			if tagMapFunc != nil {
				tag = tagMapFunc(tag)
			}

			fi.Name = name

			if tq.pp == "" || (tq.pp == "" && tag == "") {
				fi.Path = fi.Name
			} else {
				fi.Path = fmt.Sprintf("%s.%s", tq.pp, fi.Name)
			}

			// if the name is "-", disabled via a tag, skip it
			if name == "-" {
				continue
			}

			// skip unexported fields
			if len(f.PkgPath) != 0 {
				continue
			}

			// bfs search of anonymous embedded structs
			if f.Anonymous {
				var pp string
				if tag != "" {
					pp = fi.Path
				}

				fiq := fi
				fiq.Index = apnd(tq.fi.Index, fieldPos)
				queue = append(queue, typeQueue{Deref(f.Type), fiq, pp})
				fi.Embedded = true
			} else if fi.Zero.Kind() == reflect.Struct {
				fiq := fi
				fiq.Index = apnd(tq.fi.Index, fieldPos)
				queue = append(queue, typeQueue{Deref(f.Type), fiq, fiq.Path})
			}

			fiq := fi
			fiq.Index = apnd(tq.fi.Index, fieldPos)
			m = append(m, fiq)
		}
	}
	return m
}
