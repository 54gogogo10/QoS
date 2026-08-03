//go:build windows

package web

import (
	"github.com/gopacket/gopacket/pcap"
)

// listInterfaces 枚举抓包接口（Windows 用 Npcap）。
func listInterfaces() ([]apiIface, error) {
	devs, err := pcap.FindAllDevs()
	if err != nil {
		return nil, err
	}
	out := make([]apiIface, 0, len(devs))
	for _, d := range devs {
		addrs := make([]string, 0, len(d.Addresses))
		for _, a := range d.Addresses {
			if a.IP != nil && a.IP.String() != "0.0.0.0" && a.IP.String() != "::" {
				addrs = append(addrs, a.IP.String())
			}
		}
		out = append(out, apiIface{Name: d.Name, Description: d.Description, Addresses: addrs})
	}
	return out, nil
}
