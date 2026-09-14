//go:build windows

package sender

import "testing"

// TestDscpToTrafficType DSCP → QOS_TRAFFIC_TYPE 映射边界。
func TestDscpToTrafficType(t *testing.T) {
	cases := []struct {
		dscp int
		want uint32
	}{
		{0, qosTrafficBestEffort},
		{1, qosTrafficBackground},  // CS0 之上的尽力而为？归批量
		{8, qosTrafficBackground},  // CS1
		{10, qosTrafficBackground}, // AF11
		{15, qosTrafficBackground}, // 边界：<16
		{16, qosTrafficExcellentEffort}, // CS2
		{26, qosTrafficExcellentEffort}, // AF31
		{31, qosTrafficExcellentEffort}, // 边界：<32
		{32, qosTrafficAudioVideo},      // CS4
		{34, qosTrafficAudioVideo},      // AF41
		{39, qosTrafficAudioVideo},      // 边界：<40
		{40, qosTrafficVoice},           // CS5
		{46, qosTrafficVoice},           // EF
		{47, qosTrafficVoice},           // 边界：<48
		{48, qosTrafficControl},         // CS6
		{56, qosTrafficControl},         // CS7
		{63, qosTrafficControl},         // 上界
	}
	for _, c := range cases {
		if got := dscpToTrafficType(c.dscp); got != c.want {
			t.Fatalf("dscpToTrafficType(%d) = %d, want %d", c.dscp, got, c.want)
		}
	}
}
