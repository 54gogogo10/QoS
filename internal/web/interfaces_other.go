//go:build !windows

package web

import "net"

// listInterfaces 枚举抓包接口（Linux 用 net.Interfaces，纯 Go）。
func listInterfaces() ([]apiIface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]apiIface, 0, len(ifaces))
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 && ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		ips := make([]string, 0, len(addrs))
		for _, a := range addrs {
			ip := a.(*net.IPNet).IP.String()
			if ip != "0.0.0.0" && ip != "::" {
				ips = append(ips, ip)
			}
		}
		out = append(out, apiIface{Name: ifi.Name, Description: ifi.Name, Addresses: ips})
	}
	return out, nil
}
