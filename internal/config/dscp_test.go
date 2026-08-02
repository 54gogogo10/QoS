package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDSCPParseInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0", 0}, {"46", 46}, {"63", 63},
	}
	for _, c := range cases {
		var d DSCP
		if err := d.UnmarshalYAML(yamlNode(c.in, "!!int")); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if int(d) != c.want {
			t.Fatalf("%s = %d, want %d", c.in, d, c.want)
		}
	}
}

func TestDSCPParseName(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"EF", 46}, {"AF41", 34}, {"AF11", 10}, {"CS7", 56}, {"BE", 0}, {"CS0", 0},
	}
	for _, c := range cases {
		var d DSCP
		if err := d.UnmarshalYAML(yamlNode(c.in, "!!str")); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if int(d) != c.want {
			t.Fatalf("%s = %d, want %d", c.in, d, c.want)
		}
	}
}

func TestDSCPParseErrors(t *testing.T) {
	for _, in := range []string{"64", "-1", "UNKNOWN"} {
		var d DSCP
		tag := "!!int"
		if in == "UNKNOWN" {
			tag = "!!str"
		}
		if err := d.UnmarshalYAML(yamlNode(in, tag)); err == nil {
			t.Fatalf("%s: 期望报错", in)
		}
	}
}

func TestDSCPName(t *testing.T) {
	var d DSCP = 46
	if d.Name() != "EF" {
		t.Fatalf("46.Name() = %q, want EF", d.Name())
	}
	if DSCP(1).Name() != "" {
		t.Fatalf("1.Name() = %q, want empty", DSCP(1).Name())
	}
}

// yamlNode 构造一个标量 yaml.Node 供 UnmarshalYAML 测试。
func yamlNode(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}
