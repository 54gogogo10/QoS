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

func TestParseDSCP(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"46", 46, false}, {"EF", 46, false}, {"af41", 34, false}, {"CS7", 56, false},
		{" 0 ", 0, false}, {"63", 63, false},
		{"64", 0, true}, {"-1", 0, true}, {"XYZ", 0, true}, {"", 0, true},
	}
	for _, c := range cases {
		v, err := ParseDSCP(c.in)
		if c.err {
			if err == nil {
				t.Fatalf("%q: 期望报错", c.in)
			}
			continue
		}
		if err != nil || v != c.want {
			t.Fatalf("%q = %d, %v; want %d", c.in, v, err, c.want)
		}
	}
}

func TestDSCPMarshalYAML(t *testing.T) {
	var d DSCP = 46
	v, err := d.MarshalYAML()
	if err != nil || v != "EF" {
		t.Fatalf("46 应序列化为 EF, got %v, %v", v, err)
	}
	var d2 DSCP = 1
	v2, _ := d2.MarshalYAML()
	if v2 != 1 {
		t.Fatalf("1 应序列化为数字, got %v", v2)
	}
}

// TestDSCPParseNameCaseInsensitive 回归：YAML 名字与 Web/API 输入同规则（大小写不敏感）。
func TestDSCPParseNameCaseInsensitive(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"ef", 46}, {"af41", 34}, {"cs7", 56}, {"be", 0},
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
