package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DSCP 值 0-63，支持名字别名。
type DSCP int

var dscpNames = map[string]int{
	"BE": 0, "CS0": 0, "CS1": 8, "CS2": 16, "CS3": 24,
	"CS4": 32, "CS5": 40, "CS6": 48, "CS7": 56,
	"AF11": 10, "AF12": 12, "AF13": 14,
	"AF21": 18, "AF22": 20, "AF23": 22,
	"AF31": 26, "AF32": 28, "AF33": 30,
	"AF41": 34, "AF42": 36, "AF43": 38,
	"EF": 46,
}

var dscpNamesByValue = map[int]string{
	0: "BE", 8: "CS1", 10: "AF11", 12: "AF12", 14: "AF13",
	16: "CS2", 18: "AF21", 20: "AF22", 22: "AF23",
	24: "CS3", 26: "AF31", 28: "AF32", 30: "AF33",
	32: "CS4", 34: "AF41", 36: "AF42", 38: "AF43",
	40: "CS5", 46: "EF", 48: "CS6", 56: "CS7",
}

// UnmarshalYAML 接受整数 0-63 或名字（EF/AF11/CS7 等）。
func (d *DSCP) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("dscp 必须是整数 0-63 或已知名字")
	}
	switch node.Tag {
	case "!!int":
		v, err := strconv.Atoi(node.Value)
		if err != nil {
			return err
		}
		if v < 0 || v > 63 {
			return fmt.Errorf("dscp %d 超出范围 0-63", v)
		}
		*d = DSCP(v)
		return nil
	case "!!str":
		v, ok := dscpNames[node.Value]
		if !ok {
			return fmt.Errorf("未知 DSCP 名字 %q", node.Value)
		}
		*d = DSCP(v)
		return nil
	}
	return fmt.Errorf("dscp 必须是整数 0-63 或已知名字")
}

// Name 返回数值对应的常用名字，未知返回空串。
func (d DSCP) Name() string {
	return dscpNamesByValue[int(d)]
}

// ParseDSCP 解析用户输入：0-63 的数字或名字（EF/AF41/CS7 等，大小写不敏感）。
func ParseDSCP(s string) (int, error) {
	s = strings.TrimSpace(s)
	if v, ok := dscpNames[strings.ToUpper(s)]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("DSCP 必须是 0-63 的数字或已知名字（如 EF、AF41、CS7）")
	}
	if n < 0 || n > 63 {
		return 0, fmt.Errorf("DSCP %d 超出范围 0-63", n)
	}
	return n, nil
}

// MarshalYAML 序列化为常用名字（如 EF），未知值输出数字。
func (d DSCP) MarshalYAML() (interface{}, error) {
	if n := dscpNamesByValue[int(d)]; n != "" {
		return n, nil
	}
	return int(d), nil
}
