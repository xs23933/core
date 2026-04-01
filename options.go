package core

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Options map[string]any

func (opt *Options) Value(k string) (any, bool) {
	val, ok := (*opt)[k].(string)
	return val, ok
}

func (opt *Options) GetString(k string, def ...string) string {
	if val, ok := (*opt)[k]; ok && val != nil {
		if v, ok := val.(string); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func (opt *Options) GetMap(k string, def ...Options) Options {
	if val, ok := (*opt)[k]; ok && val != nil {
		if v, ok := val.(Options); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return Options{}
}

func (opt *Options) GetAs(k string, v any) error {
	if val, ok := (*opt)[k]; ok && val != nil {

		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Ptr || rv.IsNil() {
			return &InvalidUnmarshalError{reflect.TypeOf(v)}
		}
		rv = rv.Elem()

		// 关于 slice

		if rv.Type().Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Map {
			slice, ok := val.([]any)
			if !ok {
				return fmt.Errorf("cannot convert %T to slice", val)
			}
			result := reflect.MakeSlice(rv.Type(), len(slice), len(slice))
			for i, item := range slice {
				m, ok := item.(Options)
				if !ok {
					return fmt.Errorf("cannot convert %T to map", item)
				}
				result.Index(i).Set(reflect.ValueOf(m))
			}
			rv.Set(result)
			return nil
		}

		rv.Set(reflect.ValueOf(val).Convert(rv.Type()))
		return nil
	}
	return ErrDataTypeNotSupport
}

func (opt *Options) GetStrings(k string, def ...[]string) []string {
	if val, ok := (*opt)[k]; ok && val != nil {
		if v, ok := val.([]string); ok {
			return v
		}
		if v, ok := val.([]any); ok {
			s := make([]string, len(v))
			for i, v := range v {
				s[i] = fmt.Sprintf("%v", v)
			}
			return s
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return []string{}
}

func (opt *Options) GetInt(k string, def ...int) int {
	if val, ok := (*opt)[k]; ok && val != nil {
		switch v := val.(type) {
		case string:
			i, _ := strconv.Atoi(v)
			return i
		case int:
			return v
		case float64:
			return int(v)
		case int64:
			return int(v)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (opt *Options) GetInt64(k string, def ...int64) int64 {
	if val, ok := (*opt)[k]; ok && val != nil {
		switch v := val.(type) {
		case string:
			i, _ := strconv.ParseInt(v, 10, 64)
			return i
		case int:
			return int64(v)
		case float64:
			return int64(v)
		case int64:
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (opt *Options) GetBool(k string, def ...bool) bool {
	val, ok := (*opt)[k]
	if !ok {
		if len(def) > 0 {
			return def[0]
		}
		return false
	}
	switch v := val.(type) {
	case string:
		return v == "true"
	case bool:
		return v
	}
	return val.(bool)
}

func (opt *Options) ToString(k string, def ...string) string {
	if val, ok := (*opt)[k]; ok && val != nil {
		switch v := val.(type) {
		case string:
			return v
		case []byte:
			return string(v)
		case []string:
			return strings.Join(v, ",")
		case float64:
			return fmt.Sprintf("%f", v)
		case int:
			return strconv.Itoa(v)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func LoadConfigFile(file string, opts ...Options) Options {
	conf := make(Options)
	if len(opts) > 0 {
		conf = opts[0]
	}
	buf, err := os.ReadFile(file)
	if err != nil {
		conf["debug"] = true
		conf["network"] = "tcp4"
		conf["listen"] = 8080
		conf["static"] = Map{
			"static": "./static",
		}
		conf["restful"] = defaultRestful

		yml, _ := yaml.Marshal(conf)
		os.WriteFile(file, yml, 0644)
	} else if err = yaml.Unmarshal(buf, &conf); err != nil {
		log.Println(err.Error())
	}
	confFile = file
	return conf
}

func SaveConfigFile(conf map[string]any) error {
	yml, err := yaml.Marshal(conf)
	if err != nil {
		return err
	}
	return os.WriteFile(confFile, yml, 0644)
}

var (
	confFile       string
	Conf           Options
	defaultRestful = RestfulDefine{
		Data:    "data",
		Status:  "success",
		Message: "msg",
		Code:    0,
	}
)
