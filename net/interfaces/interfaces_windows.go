// Copyright (c) 2020 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package interfaces

import (
	"fmt"
	"os/exec"
	"syscall"

	"go4.org/mem"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"inet.af/netaddr"
	"tailscale.com/tsconst"
	"tailscale.com/util/lineread"
)

func init() {
	likelyHomeRouterIP = likelyHomeRouterIPWindows
}

/*
Parse out 10.0.0.1 from:

Z:\>route print -4
===========================================================================
Interface List
 15...aa 15 48 ff 1c 72 ......Red Hat VirtIO Ethernet Adapter
  5...........................Tailscale Tunnel
  1...........................Software Loopback Interface 1
===========================================================================

IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0         10.0.0.1       10.0.28.63      5
         10.0.0.0      255.255.0.0         On-link        10.0.28.63    261
       10.0.28.63  255.255.255.255         On-link        10.0.28.63    261
        10.0.42.0    255.255.255.0   100.103.42.106   100.103.42.106      5
     10.0.255.255  255.255.255.255         On-link        10.0.28.63    261
   34.193.248.174  255.255.255.255   100.103.42.106   100.103.42.106      5

*/
func likelyHomeRouterIPWindows() (ret netaddr.IP, ok bool) {
	cmd := exec.Command("route", "print", "-4")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer cmd.Wait()

	var f []mem.RO
	lineread.Reader(stdout, func(lineb []byte) error {
		line := mem.B(lineb)
		if !mem.Contains(line, mem.S("0.0.0.0")) {
			return nil
		}
		f = mem.AppendFields(f[:0], line)
		if len(f) < 3 || !f[0].EqualString("0.0.0.0") || !f[1].EqualString("0.0.0.0") {
			return nil
		}
		ipm := f[2]
		ip, err := netaddr.ParseIP(string(mem.Append(nil, ipm)))
		if err == nil && isPrivateIP(ip) {
			ret = ip
		}
		return nil
	})
	return ret, !ret.IsZero()
}

// NonTailscaleMTUs returns a map of interface LUID to interface MTU,
// for all interfaces except Tailscale tunnels.
func NonTailscaleMTUs() (map[winipcfg.LUID]uint32, error) {
	mtus := map[winipcfg.LUID]uint32{}
	ifs, err := NonTailscaleInterfaces()
	for luid, iface := range ifs {
		mtus[luid] = iface.MTU
	}
	return mtus, err
}

// NonTailscaleInterfaces returns a map of interface LUID to interface
// for all interfaces except Tailscale tunnels.
func NonTailscaleInterfaces() (map[winipcfg.LUID]*winipcfg.IPAdapterAddresses, error) {
	ifs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return nil, err
	}

	ret := map[winipcfg.LUID]*winipcfg.IPAdapterAddresses{}
	for _, iface := range ifs {
		if iface.Description() == tsconst.WintunInterfaceDesc {
			continue
		}
		ret[iface.LUID] = iface
	}

	return ret, nil
}

// GetWindowsDefault returns the interface that has the non-Tailscale
// default route for the given address family.
//
// It returns (nil, nil) if no interface is found.
func GetWindowsDefault(family winipcfg.AddressFamily) (*winipcfg.IPAdapterAddresses, error) {
	ifs, err := NonTailscaleInterfaces()
	if err != nil {
		return nil, err
	}

	routes, err := winipcfg.GetIPForwardTable2(family)
	if err != nil {
		return nil, err
	}

	bestMetric := ^uint32(0)
	var bestIface *winipcfg.IPAdapterAddresses
	for _, route := range routes {
		iface := ifs[route.InterfaceLUID]
		if route.DestinationPrefix.PrefixLength != 0 || iface == nil {
			continue
		}
		if iface.OperStatus == winipcfg.IfOperStatusUp && route.Metric < bestMetric {
			bestMetric = route.Metric
			bestIface = iface
		}
	}

	return bestIface, nil
}

func DefaultRouteInterface() (string, error) {
	iface, err := GetWindowsDefault(windows.AF_INET)
	if err != nil {
		return "", err
	}
	if iface == nil {
		return "(none)", nil
	}
	return fmt.Sprintf("%s (%s)", iface.FriendlyName(), iface.Description()), nil
}
