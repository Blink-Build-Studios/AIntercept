package state

import (
	"reflect"
	"regexp"
	"testing"
)

// Every stored row type names its columns: each field (an embedded struct's
// fields included) has a snake_case parquet tag, so a table file's columns
// aren't Go field names.
func TestEveryRowFieldHasAParquetTag(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	var check func(path string, ty reflect.Type)
	check = func(path string, ty reflect.Type) {
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				check(path+"."+f.Name, f.Type)
				continue
			}
			tag := f.Tag.Get("parquet")
			name := tag
			for j := range tag {
				if tag[j] == ',' {
					name = tag[:j]
					break
				}
			}
			if name == "-" {
				continue
			}
			if !snake.MatchString(name) {
				t.Errorf("%s.%s: parquet tag %q isn't a snake_case column name", path, f.Name, tag)
			}
			if f.Type.Kind() == reflect.Struct {
				check(path+"."+f.Name, f.Type)
			}
		}
	}
	for _, v := range RowTypes {
		ty := reflect.TypeOf(v)
		check(ty.Name(), ty)
	}
}
