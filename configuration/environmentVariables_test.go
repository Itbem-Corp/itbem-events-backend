package configuration

import (
	"reflect"
	"testing"
)

func TestSetConfigFieldSupportsTypedOptionalConfiguration(t *testing.T) {
	type config struct {
		Text    string
		Reserve int
		Enabled bool
	}
	value := reflect.ValueOf(&config{}).Elem()
	if err := setConfigField(value.FieldByName("Text"), "TEXT", "value"); err != nil {
		t.Fatal(err)
	}
	if err := setConfigField(value.FieldByName("Reserve"), "RESERVE", "123"); err != nil {
		t.Fatal(err)
	}
	if err := setConfigField(value.FieldByName("Enabled"), "ENABLED", "true"); err != nil {
		t.Fatal(err)
	}
	got := value.Interface().(config)
	if got.Text != "value" || got.Reserve != 123 || !got.Enabled {
		t.Fatalf("unexpected typed configuration: %#v", got)
	}
}

func TestSetConfigFieldRejectsMalformedTypedConfiguration(t *testing.T) {
	integer := reflect.ValueOf(new(int)).Elem()
	if err := setConfigField(integer, "RESERVE", "not-a-number"); err == nil {
		t.Fatal("malformed integer was accepted")
	}
	boolean := reflect.ValueOf(new(bool)).Elem()
	if err := setConfigField(boolean, "ENABLED", "perhaps"); err == nil {
		t.Fatal("malformed boolean was accepted")
	}
}
