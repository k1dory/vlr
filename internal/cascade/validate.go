package cascade

import (
	"fmt"
	"net"
	"regexp"
)

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,15}$`)

// Values inserted into the remote shell script must be names, ports or IPv4
// addresses, never shell expressions. The provisioned exit is IPv4 NAT only.
func ValidateExitParams(p ExitProvisionParams) error {
	if !interfaceName.MatchString(p.Iface) || p.Iface == "." || p.Iface == ".." {
		return fmt.Errorf("invalid tunnel interface name")
	}
	if p.WAN != "" && !interfaceName.MatchString(p.WAN) {
		return fmt.Errorf("invalid WAN interface name")
	}
	if p.WGPort < 1 || p.WGPort > 65535 {
		return fmt.Errorf("invalid AWG port")
	}
	ip, _, err := net.ParseCIDR(p.EUAddress)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("EU tunnel address must be IPv4 CIDR")
	}
	ru := net.ParseIP(p.RUTunnelIP)
	if ru == nil || ru.To4() == nil || ru.Equal(ip) {
		return fmt.Errorf("RU tunnel address must be a distinct IPv4 address")
	}
	return nil
}
