package taurus

import (
	"encoding"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/spf13/pflag"
)

var (
	envPrefix          string
	expandEnv          bool
	envAliases         = map[string][]string{}
	flags              = map[string]*pflag.Flag{}
	customMarshalers   = map[reflect.Type]func(interface{}) ([]byte, error){}
	customUnmarshalers = map[reflect.Type]func(interface{}, []byte) error{}
)

func SetEnvPrefix(prefix string) {
	envPrefix = prefix
}

func SetExpandEnv(expand bool) {
	expandEnv = expand
}

func BindEnvAlias(fieldPath string, aliases ...string) {
	envAliases[fieldPath] = append(envAliases[fieldPath], aliases...)
}

func BindFlag(fieldPath string, flag *pflag.Flag) {
	flags[fieldPath] = flag
}

func RegisterCustomMarshaler[T any](marshaler func(T) ([]byte, error)) {
	customMarshalers[reflect.TypeFor[T]()] = func(v interface{}) ([]byte, error) {
		return marshaler(v.(T))
	}

	yaml.RegisterCustomMarshaler(func(t T) ([]byte, error) {
		return marshaler(t)
	})
}

func RegisterCustomUnmarshaler[T any](unmarshaler func(*T, []byte) error) {
	customUnmarshalers[reflect.TypeFor[T]()] = func(v interface{}, data []byte) error {
		return unmarshaler(v.(*T), data)
	}

	yaml.RegisterCustomUnmarshaler(func(t *T, data []byte) error {
		return unmarshaler(t, data)
	})
}

func Load(configData string, cfg interface{}) error {
	data := configData
	if expandEnv {
		data = os.ExpandEnv(data)
	}

	if err := yaml.Unmarshal([]byte(data), cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config data: %w", err)
	}
	return nil
}

func LoadFile(configFilePath string, cfg interface{}) error {
	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	return Load(string(configData), cfg)
}

func LoadEnv(prefix string, cfg interface{}) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("cfg must be a pointer to a struct")
	}

	v = v.Elem()
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		// Get the `yaml` tag or default to field name
		tag := field.Tag.Get("yaml")
		if tag == "" {
			tag = field.Name
		}
		envKey := prefix + "_" + strings.ToUpper(tag)

		if field.Type.Kind() == reflect.Struct {
			if err := LoadEnv(envKey, fieldValue.Addr().Interface()); err != nil {
				return err
			}
			continue
		}

		if !fieldValue.CanSet() {
			continue
		}

		envVal, exists := os.LookupEnv(envKey)
		if !exists {
			for _, key := range envAliases[strings.TrimPrefix(envKey, envPrefix+"_")] {
				envVal, exists = os.LookupEnv(envPrefix + "_" + key)
				if exists {
					envKey = envPrefix + "_" + key
					break
				}
			}
		}
		if !exists {
			continue
		}

		if err := setField(field, fieldValue, envVal); err != nil {
			return fmt.Errorf("error setting field for %s: %v", envKey, err)
		}
	}
	return nil
}

func LoadFlags(prefix string, cfg interface{}) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("cfg must be a pointer to a struct")
	}

	v = v.Elem()
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		fieldPath := field.Name
		if prefix != "" {
			fieldPath = prefix + "." + fieldPath
		}

		if field.Type.Kind() == reflect.Struct {
			if err := LoadFlags(fieldPath, fieldValue.Addr().Interface()); err != nil {
				return err
			}
			continue
		}

		if !fieldValue.CanSet() {
			continue
		}

		fl, exists := flags[fieldPath]
		if !exists || !fl.Changed {
			continue
		}

		if err := setField(field, fieldValue, fl.Value.String()); err != nil {
			return fmt.Errorf("error setting field for %s: %v", fieldPath, err)
		}
	}
	return nil
}

func setField(field reflect.StructField, fieldValue reflect.Value, value string) error {
	if customUnmarshaler, exists := customUnmarshalers[field.Type]; exists {
		if err := customUnmarshaler(fieldValue.Addr().Interface(), []byte(value)); err != nil {
			return fmt.Errorf("failed to unmarshal custom field: %v", err)
		}
		return nil
	}

	if unmarshaler, ok := fieldValue.Addr().Interface().(encoding.TextUnmarshaler); ok {
		if err := unmarshaler.UnmarshalText([]byte(value)); err != nil {
			return fmt.Errorf("failed to unmarshal field: %v", err)
		}
		return nil
	}

	switch fieldValue.Kind() {
	case reflect.String:
		fieldValue.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		val, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int: %v", err)
		}
		fieldValue.SetInt(val)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		val, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid uint: %v", err)
		}
		fieldValue.SetUint(val)
	case reflect.Float32, reflect.Float64:
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float: %v", err)
		}
		fieldValue.SetFloat(val)
	case reflect.Bool:
		val, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool: %v", err)
		}
		fieldValue.SetBool(val)
	default:
		return fmt.Errorf("unsupported field type: %s", field.Type.Kind())
	}
	return nil
}
