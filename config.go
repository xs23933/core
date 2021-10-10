package core

import (
	"io/ioutil"
	"log"
	"strconv"

	"gopkg.in/yaml.v3"
)

type config map[string]interface{}

func (c *config) Value(k string) (interface{}, bool) {
	val, ok := (*c)[k].(string)
	return val, ok
}

func (c *config) GetString(k string, def ...string) string {
	if val, ok := (*c)[k]; ok && val != nil {
		if v, ok := val.(string); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func (c *config) GetMap(k string, def ...config) config {
	if val, ok := (*c)[k]; ok && val != nil {
		if v, ok := val.(config); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return config{}
}

func (c *config) GetStrings(k string, def ...[]string) []string {
	if val, ok := (*c)[k]; ok && val != nil {
		if v, ok := val.([]string); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return []string{}
}

func (c *config) GetInt(k string, def ...int) int {
	if val, ok := (*c)[k]; ok && val != nil {
		if v := val.(int); ok {
			return v
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (c *config) GetInt64(k string, def ...int64) int64 {
	if val, ok := (*c)[k]; ok && val != nil {
		if v := val.(int); ok {
			return int64(v)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (c *config) GetBool(k string, def ...bool) bool {
	val, ok := (*c)[k]
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

func (c *config) ToString(k string, def ...string) string {
	if val, ok := (*c)[k]; ok && val != nil {
		switch v := val.(type) {
		case string:
			return v
		case []byte:
			return string(v)
		case int:
			return strconv.Itoa(v)
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

func LoadConfigFile(file string) config {
	conf := make(config)
	buf, err := ioutil.ReadFile(file)
	if err != nil {
		conf["debug"] = true
		conf["listen"] = 8080
		conf["maxMultipartMemory"] = defaultMultipartMemory
		conf["static"] = map[string]string{
			"/favicon": "./static",
			"/js":      "./static/js",
			"/css":     "./static/css",
			"/assets":  "./static/assets",
		}
		yml, _ := yaml.Marshal(conf)
		ioutil.WriteFile(file, yml, 0644)
	} else if err = yaml.Unmarshal(buf, &conf); err != nil {
		log.Fatalf(err.Error())
	}
	confFile = file
	Conf = conf
	return conf
}

func SaveConfigFile(conf map[string]interface{}) error {
	yml, err := yaml.Marshal(conf)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(confFile, yml, 0644)
}

var (
	confFile string
	Conf     config
)
