package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/peterh/liner"

	"fs"
)

func cstr(in interface{}) string {
	v := reflect.ValueOf(in)
	for v.Kind() == reflect.Ptr {
		v = reflect.Indirect(v)
	}

	var s string
	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			panic("argument to \"str\" is not a string")
		}
		s = string(v.Slice(0, v.Len()).Bytes())
	case reflect.String:
		s = v.String()
	default:
		panic("argument to \"str\" is not a string")
	}
	i := 0
	for i < len(s) && s[i] != 0 {
		i++
	}
	return s[:i]
}

func main() {
	fsshm, err := fs.NewFieldSystem()
	if err != nil {
		panic(err)
	}

	terp := NewInterpreter()
	terp.Tag = "json"
	terp.Global("fs", fsshm)
	terp.Global("str", cstr)

	lr := liner.NewLiner()
	defer lr.Close()
	encoder := json.NewEncoder(os.Stdout)
	// encoder.SetIndent("", " ")

	for {
		line, err := lr.Prompt("> ")
		if err != nil { // io.EOF
			break
		}
		lr.AppendHistory(line)
		for _, line = range strings.Split(line, ";") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			value, err := terp.Eval(line)
			if err != nil {
				fmt.Println(err)
				continue
			}
			if !value.IsValid() {
				continue
			}

			if value.Kind() == reflect.Func && value.Type().NumIn() == 0 {
				out := value.Call([]reflect.Value{})
				if len(out) > 0 {
					value = out[0]
				}
			}

			err = encoder.Encode(value.Interface())
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
