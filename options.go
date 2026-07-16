package core

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gopkg.in/yaml.v3"
)

type Options map[string]any

func (opt *Options) Value(k string) (any, bool) {
	val, ok := (*opt)[k].(string)
	return val, ok
}

func (opt *Options) GetString(k string, def ...string) string {
	if strings.Contains(k, ".") {
		return opt.GetPathString(k, def...)
	}
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

func (d Options) As(k string, v any) error {
	if val, ok := d[k]; ok && val != nil {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Pointer || rv.IsNil() {
			return &InvalidUnmarshalError{reflect.TypeOf(v)}
		}
		// 先尝试直接转换
		srcVal := reflect.ValueOf(val)
		srcType := srcVal.Type()
		dstType := rv.Elem().Type()

		if srcType.ConvertibleTo(dstType) {
			rv.Elem().Set(srcVal.Convert(dstType))
			return nil
		}

		// 如果不能直接转换，使用 JSON 序列化/反序列化
		buf, err := sonic.Marshal(val)
		if err != nil {
			return err
		}
		return sonic.Unmarshal(buf, v)
	}
	return ErrDataTypeNotSupport
}

func (opt *Options) GetStrings(k string, def ...[]string) []string {
	if strings.Contains(k, ".") {
		return opt.GetPathStrings(k, def...)
	}
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
	if strings.Contains(k, ".") {
		return opt.GetPathInt(k, def...)
	}
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

func (opt *Options) GetDuration(k string, def ...time.Duration) time.Duration {
	val, ok := opt.getValue(k)
	if ok {
		switch v := val.(type) {
		case time.Duration:
			return v
		case string:
			if duration, err := time.ParseDuration(v); err == nil {
				return duration
			}
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

func (opt *Options) getValue(k string) (any, bool) {
	keys := strings.Split(k, ".")
	current := *opt
	for i, key := range keys {
		val, ok := current[key]
		if !ok {
			return nil, false
		}
		if i == len(keys)-1 {
			return val, true
		}
		switch next := val.(type) {
		case Options:
			current = next
		case map[string]any:
			current = Options(next)
		default:
			return nil, false
		}
	}
	return nil, false
}

func (opt *Options) GetBool(k string, def ...bool) bool {
	if strings.Contains(k, ".") {
		return opt.GetPathBool(k, def...)
	}
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

// 初始化函数，在 flag.Parse 之前处理
func init() {
	// 检查是否有 -generate 参数
	for _, arg := range os.Args[1:] {
		if arg == "--generate" || arg == "-generate" {
			// 找到 -generate 后面的配置文件参数
			configFile := "config.dat" // 默认值
			for j := 0; j < len(os.Args[1:]); j++ {
				if os.Args[1:][j] == "-f" && j+1 < len(os.Args[1:]) {
					configFile = os.Args[1:][j+1]
					break
				}
			}

			// 执行生成逻辑
			if ok := checkGenerate(configFile); !ok {
				fmt.Fprintf(os.Stderr, "Generate config failed: %v\n", ok)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func LoadConfigFile(file string, opts ...Options) Options {
	conf := make(Options)
	if len(opts) > 0 {
		conf = opts[0]
	}

	// 检查 --generate 标志
	// if checkGenerate(file) {
	// 	os.Exit(0)
	// }

	if strings.HasSuffix(file, ".dat") {
		return loadEncryptedConfig(file, conf)
	}

	return loadYamlConfig(file, conf)
}

// checkGenerate 检查命令行是否带 --generate 标志
// 如果带 --generate 且配置文件是 .dat 结尾，则生成加密 .dat 后返回 true
// 如果带 --generate 且配置文件是 .yaml/.yml 结尾，则生成同名 .dat 后返回 true
func checkGenerate(configFile string) bool {
	hasGenerate := false
	for _, arg := range os.Args[1:] {
		if arg == "--generate" || arg == "-generate" {
			hasGenerate = true
			break
		}
	}
	if !hasGenerate {
		return false
	}

	if strings.HasSuffix(configFile, ".dat") {
		// 从 .dat 推导出 .default.yaml
		yamlFile, _ := strings.CutSuffix(configFile, ".dat")
		yamlFile += ".default.yaml"

		yamlBuf, err := os.ReadFile(yamlFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[generate] read %s failed: %v\n", yamlFile, err)
			os.Exit(1)
		}

		encryptedBuf, err := EncryptAESBytes(yamlBuf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[generate] encrypt failed: %v\n", err)
			os.Exit(1)
		}

		if err := os.WriteFile(configFile, encryptedBuf, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[generate] write %s failed: %v\n", configFile, err)
			os.Exit(1)
		}

		fmt.Fprintf(os.Stdout, "[generate] %s -> %s OK\n", yamlFile, configFile)
		return true
	}

	if strings.HasSuffix(configFile, ".yaml") || strings.HasSuffix(configFile, ".yml") {
		// 从 .yaml 生成同名 .dat
		datFile, _ := strings.CutSuffix(configFile, filepath.Ext(configFile))
		datFile += ".dat"

		yamlBuf, err := os.ReadFile(configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[generate] read %s failed: %v\n", configFile, err)
			os.Exit(1)
		}

		encryptedBuf, err := EncryptAESBytes(yamlBuf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[generate] encrypt failed: %v\n", err)
			os.Exit(1)
		}

		if err := os.WriteFile(datFile, encryptedBuf, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[generate] write %s failed: %v\n", datFile, err)
			os.Exit(1)
		}

		fmt.Fprintf(os.Stdout, "[generate] %s -> %s OK\n", configFile, datFile)
		return true
	}

	fmt.Fprintf(os.Stderr, "[generate] config file must be .dat or .yaml/.yml, got: %s\n", configFile)
	os.Exit(1)
	return false
}

func loadEncryptedConfig(datFile string, defaultConf Options) Options {
	yamlFile, _ := strings.CutSuffix(datFile, ".dat")
	yamlFile += ".default.yaml"

	if yamlBuf, err := os.ReadFile(yamlFile); err == nil {
		D("Update config(%s) from %s", datFile, yamlFile)
		if encryptedBuf, err := EncryptAESBytes(yamlBuf); err == nil {
			if err := os.WriteFile(datFile, encryptedBuf, 0644); err != nil {
				Erro("Write %s failed: %v", datFile, err)
			}
		} else {
			Erro("Encrypt %s failed: %v", datFile, err)
		}

		// 解析 yaml
		var conf Options
		if err := yaml.Unmarshal(yamlBuf, &conf); err != nil {
			return defaultConf
		}
		confFile = datFile
		return conf
	}

	encryptedBuf, err := os.ReadFile(datFile)
	if err != nil {
		return createDefaultConfig(yamlFile, datFile)
	}

	decryptedBuf, err := DecryptAESBytes(encryptedBuf)
	if err != nil {
		// 解密失败，重新创建配置
		return createDefaultConfig(yamlFile, datFile)
	}

	var conf Options
	if err := yaml.Unmarshal(decryptedBuf, &conf); err != nil {
		return defaultConf
	}

	confFile = datFile
	return conf
}

func loadYamlConfig(yamlFile string, defaultConf Options) Options {
	buf, err := os.ReadFile(yamlFile)
	if err != nil {
		return createDefaultConfig(yamlFile, "")
	}

	var conf Options
	if err := yaml.Unmarshal(buf, &conf); err != nil {
		return defaultConf
	}

	confFile = yamlFile
	return conf
}

func createDefaultConfig(yamlFile, datFile string) Options {
	conf := make(Options)
	conf["debug"] = true
	conf["network"] = "tcp4"
	conf["listen"] = 8080
	conf["static"] = Map{
		"static": "./static",
	}
	conf["restful"] = defaultRestful
	conf["colorful"] = true

	yml, _ := yaml.Marshal(conf)

	// 写入 yaml 文件（明文）
	if yamlFile != "" {
		if err := os.WriteFile(yamlFile, yml, 0644); err != nil {
			Erro("write %s failed: %v", yamlFile, err)
		}
		confFile = yamlFile
	}

	// 写入 dat 文件（加密）
	if datFile != "" {
		encryptedBuf, _ := EncryptAESBytes(yml)
		if err := os.WriteFile(datFile, encryptedBuf, 0644); err != nil {
			Erro("write %s failed: %v", datFile, err)
		}
		confFile = datFile
	}

	return conf
}

func SaveConfigFile(conf map[string]any) error {
	yml, err := yaml.Marshal(conf)
	if err != nil {
		return err
	}
	if strings.HasSuffix(confFile, ".dat") {
		yml, err = EncryptAESBytes(yml)
		if err != nil {
			return err
		}
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

// GetPathString 支持通过点号路径获取嵌套配置
// 例如: GetPathString("telegram.token") 会获取 cfg["telegram"].(Options)["token"]
func (opt *Options) GetPathString(path string, def ...string) string {
	keys := strings.Split(path, ".")
	current := *opt

	for i, key := range keys {
		val, exists := current[key]
		if !exists {
			if len(def) > 0 {
				return def[0]
			}
			return ""
		}

		// 如果是最后一个key，尝试转换为string
		if i == len(keys)-1 {
			if str, ok := val.(string); ok {
				return str
			}
			if len(def) > 0 {
				return def[0]
			}
			return ""
		}

		// 如果不是最后一个key，需要继续向下查找
		next, ok := val.(Options)
		if !ok {
			// 尝试从 map[string]any 转换
			if m, ok := val.(map[string]any); ok {
				next = Options(m)
			} else {
				if len(def) > 0 {
					return def[0]
				}
				return ""
			}
		}
		current = next
	}

	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// 同样添加其他类型的路径访问方法
func (opt *Options) GetPathInt(path string, def ...int) int {
	keys := strings.Split(path, ".")
	current := *opt

	for i, key := range keys {
		val, exists := current[key]
		if !exists {
			if len(def) > 0 {
				return def[0]
			}
			return 0
		}

		if i == len(keys)-1 {
			switch v := val.(type) {
			case string:
				if i, err := strconv.Atoi(v); err == nil {
					return i
				}
			case int:
				return v
			case float64:
				return int(v)
			case int64:
				return int(v)
			}
			if len(def) > 0 {
				return def[0]
			}
			return 0
		}

		next, ok := val.(Options)
		if !ok {
			if m, ok := val.(map[string]any); ok {
				next = Options(m)
			} else {
				if len(def) > 0 {
					return def[0]
				}
				return 0
			}
		}
		current = next
	}

	if len(def) > 0 {
		return def[0]
	}
	return 0
}

// GetPathBool 获取布尔值
func (opt *Options) GetPathBool(path string, def ...bool) bool {
	keys := strings.Split(path, ".")
	current := *opt

	for i, key := range keys {
		val, exists := current[key]
		if !exists {
			if len(def) > 0 {
				return def[0]
			}
			return false
		}

		if i == len(keys)-1 {
			switch v := val.(type) {
			case string:
				return v == "true" || v == "1"
			case bool:
				return v
			case int:
				return v != 0
			}
			if len(def) > 0 {
				return def[0]
			}
			return false
		}

		next, ok := val.(Options)
		if !ok {
			if m, ok := val.(map[string]any); ok {
				next = Options(m)
			} else {
				if len(def) > 0 {
					return def[0]
				}
				return false
			}
		}
		current = next
	}

	if len(def) > 0 {
		return def[0]
	}
	return false
}

// GetPathStrings 获取字符串数组类型的配置值
func (opt *Options) GetPathStrings(path string, def ...[]string) []string {
	keys := strings.Split(path, ".")
	return opt.getPathStringsRecursive(keys, def...)
}

func (opt *Options) getPathStringsRecursive(keys []string, def ...[]string) []string {
	if len(keys) == 0 {
		if len(def) > 0 {
			return def[0]
		}
		return []string{}
	}

	current, exists := (*opt)[keys[0]]
	if !exists {
		if len(def) > 0 {
			return def[0]
		}
		return []string{}
	}

	// 如果是最后一层
	if len(keys) == 1 {
		switch v := current.(type) {
		case []string:
			return v
		case []any:
			result := make([]string, len(v))
			for i, item := range v {
				result[i] = fmt.Sprintf("%v", item)
			}
			return result
		default:
			if len(def) > 0 {
				return def[0]
			}
			return []string{}
		}
	}

	// 继续向下查找
	var nextOpt Options
	switch v := current.(type) {
	case Options:
		nextOpt = v
	case map[string]any:
		nextOpt = Options(v)
	default:
		if len(def) > 0 {
			return def[0]
		}
		return []string{}
	}

	return nextOpt.getPathStringsRecursive(keys[1:], def...)
}
