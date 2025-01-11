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

var t *Taurus

func init() {
	t = New()
}

type Taurus struct {
	envPrefix          string
	expandEnv          bool
	envAliases         map[string][]string
	flags              map[string]*pflag.Flag
	customMarshalers   map[reflect.Type]func(interface{}) ([]byte, error)
	customUnmarshalers map[reflect.Type]func(interface{}, []byte) error
}

func New() *Taurus {
	return &Taurus{
		envAliases:         make(map[string][]string),
		flags:              make(map[string]*pflag.Flag),
		customMarshalers:   make(map[reflect.Type]func(interface{}) ([]byte, error)),
		customUnmarshalers: make(map[reflect.Type]func(interface{}, []byte) error),
	}
}

func SetEnvPrefix(prefix string) {
	t.SetEnvPrefix(prefix)
}

func (t *Taurus) SetEnvPrefix(prefix string) {
	t.envPrefix = prefix
}

func SetExpandEnv(expand bool) {
	t.SetExpandEnv(expand)
}

func (t *Taurus) SetExpandEnv(expand bool) {
	t.expandEnv = expand
}

func BindEnvAlias(fieldPath string, aliases ...string) {
	t.BindEnvAlias(fieldPath, aliases...)
}

func (t *Taurus) BindEnvAlias(fieldPath string, aliases ...string) {
	t.envAliases[fieldPath] = append(t.envAliases[fieldPath], aliases...)
}

func BindFlag(fieldPath string, flag *pflag.Flag) {
	t.BindFlag(fieldPath, flag)
}

func (t *Taurus) BindFlag(fieldPath string, flag *pflag.Flag) {
	t.flags[fieldPath] = flag
}

// RegisterMarshaler registers a custom marshaler for the type argument T.
// If the parameter "t" is nil, the default Taurus instance is used.
func RegisterMarshaler[T any](t *Taurus, marshaler func(T) ([]byte, error)) {
	t.customMarshalers[reflect.TypeFor[T]()] = func(v interface{}) ([]byte, error) {
		return marshaler(v.(T))
	}

	yaml.RegisterCustomMarshaler(func(v T) ([]byte, error) {
		return marshaler(v)
	})
}

// RegisterUnmarshaler registers a custom unmarshaler for the type argument T.
// If the parameter "t" is nil, the default Taurus instance is used.
func RegisterUnmarshaler[T any](t *Taurus, unmarshaler func(*T, []byte) error) {
	t.customUnmarshalers[reflect.TypeFor[T]()] = func(v interface{}, data []byte) error {
		return unmarshaler(v.(*T), data)
	}

	yaml.RegisterCustomUnmarshaler(func(v *T, data []byte) error {
		return unmarshaler(v, data)
	})
}

func Load(configData string, cfg interface{}) error {
	return t.Load(configData, cfg)
}

func (t *Taurus) Load(configData string, cfg interface{}) error {
	data := configData
	if t.expandEnv {
		data = os.ExpandEnv(data)
	}

	if err := yaml.Unmarshal([]byte(data), cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config data: %w", err)
	}
	return nil
}

func LoadFile(configFilePath string, cfg interface{}) error {
	return t.LoadFile(configFilePath, cfg)
}

func (t *Taurus) LoadFile(configFilePath string, cfg interface{}) error {
	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	return t.Load(string(configData), cfg)
}

func LoadEnv(prefix string, cfg interface{}) error {
	return t.LoadEnv(prefix, cfg)
}

func (t *Taurus) LoadEnv(prefix string, cfg interface{}) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("cfg must be a pointer to a struct")
	}

	v = v.Elem()
	ty := v.Type()

	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		fieldValue := v.Field(i)

		// Get the `yaml` tag or default to field name
		tag := field.Tag.Get("yaml")
		if tag == "" {
			tag = field.Name
		}
		envKey := strings.ToUpper(tag)
		if prefix != "" {
			envKey = prefix + "_" + envKey
		}

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
			for _, key := range t.envAliases[strings.TrimPrefix(envKey, t.envPrefix+"_")] {
				envVal, exists = os.LookupEnv(t.envPrefix + "_" + key)
				if exists {
					envKey = t.envPrefix + "_" + key
					break
				}
			}
		}
		if !exists {
			continue
		}

		if err := t.setField(field, fieldValue, envVal); err != nil {
			return fmt.Errorf("error setting field for %s: %v", envKey, err)
		}
	}
	return nil
}

func LoadFlags(cfg interface{}) error {
	return t.LoadFlags(cfg)
}

func (t *Taurus) LoadFlags(cfg interface{}) error {
	return t.loadFlags("", cfg)
}

func (t *Taurus) loadFlags(prefix string, cfg interface{}) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("cfg must be a pointer to a struct")
	}

	v = v.Elem()
	ty := v.Type()

	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		fieldValue := v.Field(i)

		fieldPath := field.Name
		if prefix != "" {
			fieldPath = prefix + "." + fieldPath
		}

		if field.Type.Kind() == reflect.Struct {
			if err := t.loadFlags(fieldPath, fieldValue.Addr().Interface()); err != nil {
				return err
			}
			continue
		}

		if !fieldValue.CanSet() {
			continue
		}

		fl, exists := t.flags[fieldPath]
		if !exists || !fl.Changed {
			continue
		}

		if err := t.setField(field, fieldValue, fl.Value.String()); err != nil {
			return fmt.Errorf("error setting field for %s: %v", fieldPath, err)
		}
	}
	return nil
}

func (t *Taurus) setField(field reflect.StructField, fieldValue reflect.Value, value string) error {
	if customUnmarshaler, exists := t.customUnmarshalers[field.Type]; exists {
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
