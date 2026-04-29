package main

import (
	"fmt"
	"math/big"
	"net"
	"strings"
)

// Cidr is a parsed CIDR with the address held as a big.Int so the
// same code path handles v4 (32 bits) and v6 (128 bits). Mirror of
// Android's CidrMath.Cidr.
type Cidr struct {
	Addr   *big.Int
	Prefix int
	Bits   int // 32 (v4) or 128 (v6)
}

// IsV4 reports whether this CIDR is in the IPv4 family.
func (c Cidr) IsV4() bool { return c.Bits == 32 }

// Start returns the first address in the range (inclusive).
func (c Cidr) Start() *big.Int { return new(big.Int).Set(c.Addr) }

// End returns the last address in the range (inclusive).
func (c Cidr) End() *big.Int {
	hostBits := c.Bits - c.Prefix
	if hostBits == 0 {
		return new(big.Int).Set(c.Addr)
	}
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(hostBits)), big.NewInt(1))
	return new(big.Int).Or(c.Addr, mask)
}

// String returns the canonical "ip/prefix" form.
func (c Cidr) String() string {
	ip := bigIntToIP(c.Addr, c.Bits)
	return fmt.Sprintf("%s/%d", ip.String(), c.Prefix)
}

// ParseCidr parses "10.0.0.0/8" or "fe80::/10" into a Cidr. Plain
// IPs (no slash) accepted as /32 (v4) or /128 (v6). Returns an
// error for malformed input.
func ParseCidr(s string) (Cidr, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return Cidr{}, fmt.Errorf("empty CIDR")
	}
	addrStr := t
	prefStr := ""
	if i := strings.IndexByte(t, '/'); i >= 0 {
		addrStr = t[:i]
		prefStr = t[i+1:]
	}
	ip := net.ParseIP(addrStr)
	if ip == nil {
		return Cidr{}, fmt.Errorf("invalid IP: %s", addrStr)
	}
	var bits int
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		bits = 32
	} else {
		bits = 128
	}
	prefix := bits
	if prefStr != "" {
		p, err := parsePrefix(prefStr)
		if err != nil {
			return Cidr{}, err
		}
		prefix = p
	}
	if prefix < 0 || prefix > bits {
		return Cidr{}, fmt.Errorf("prefix %d out of range for %d-bit family", prefix, bits)
	}
	raw := new(big.Int).SetBytes(ip)
	hostBits := bits - prefix
	masked := new(big.Int).Set(raw)
	if hostBits > 0 {
		masked.Rsh(masked, uint(hostBits))
		masked.Lsh(masked, uint(hostBits))
	}
	return Cidr{Addr: masked, Prefix: prefix, Bits: bits}, nil
}

func parsePrefix(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid prefix: %s", s)
	}
	return n, nil
}

// SubtractFromUniverse returns the complement of bypass within
// 0.0.0.0/0 (v4) and ::/0 (v6) combined. Each family is processed
// independently. Empty bypass returns [0.0.0.0/0, ::/0].
func SubtractFromUniverse(bypass []Cidr) []Cidr {
	v4 := make([]Cidr, 0, len(bypass))
	v6 := make([]Cidr, 0, len(bypass))
	for _, c := range bypass {
		if c.IsV4() {
			v4 = append(v4, c)
		} else {
			v6 = append(v6, c)
		}
	}
	out := make([]Cidr, 0)
	v4End := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 32), big.NewInt(1))
	out = append(out, subtractWithinFamily(big.NewInt(0), v4End, v4, 32)...)
	v6End := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	out = append(out, subtractWithinFamily(big.NewInt(0), v6End, v6, 128)...)
	return out
}

// subtractWithinFamily: range-based subtraction for one family.
// Treat [start, end] as a list of "kept" intervals; for each bypass
// CIDR split intervals around the bypass range. Convert surviving
// intervals to CIDRs.
func subtractWithinFamily(start, end *big.Int, bypass []Cidr, bits int) []Cidr {
	type rng struct{ s, e *big.Int }
	kept := []rng{{new(big.Int).Set(start), new(big.Int).Set(end)}}
	one := big.NewInt(1)
	for _, b := range bypass {
		bs, be := b.Start(), b.End()
		next := make([]rng, 0, len(kept))
		for _, r := range kept {
			s, e := r.s, r.e
			if be.Cmp(s) < 0 || bs.Cmp(e) > 0 {
				next = append(next, r) // no overlap
				continue
			}
			if bs.Cmp(s) <= 0 && be.Cmp(e) >= 0 {
				continue // bypass fully covers this kept range
			}
			if bs.Cmp(s) <= 0 && be.Cmp(e) < 0 {
				// clip left
				next = append(next, rng{new(big.Int).Add(be, one), e})
				continue
			}
			if bs.Cmp(s) > 0 && be.Cmp(e) >= 0 {
				// clip right
				next = append(next, rng{s, new(big.Int).Sub(bs, one)})
				continue
			}
			// split middle
			next = append(next,
				rng{s, new(big.Int).Sub(bs, one)},
				rng{new(big.Int).Add(be, one), e},
			)
		}
		kept = next
	}
	out := make([]Cidr, 0, len(kept))
	for _, r := range kept {
		out = append(out, rangeToCidrs(r.s, r.e, bits)...)
	}
	return out
}

// rangeToCidrs decomposes [start, end] into the smallest set of
// aligned CIDRs.
func rangeToCidrs(start, end *big.Int, bits int) []Cidr {
	out := []Cidr{}
	cur := new(big.Int).Set(start)
	one := big.NewInt(1)
	for cur.Cmp(end) <= 0 {
		var maxByAlign int
		if cur.Sign() == 0 {
			maxByAlign = bits
		} else {
			// big.Int has no LowestSetBit; emulate via TrailingZeroBits-style.
			tmp := new(big.Int).Set(cur)
			maxByAlign = 0
			for tmp.Bit(0) == 0 && tmp.Sign() != 0 {
				tmp.Rsh(tmp, 1)
				maxByAlign++
			}
			if maxByAlign > bits {
				maxByAlign = bits
			}
		}
		rangeLen := new(big.Int).Sub(new(big.Int).Add(end, one), cur)
		maxByLen := rangeLen.BitLen() - 1
		sizeBits := maxByAlign
		if maxByLen < sizeBits {
			sizeBits = maxByLen
		}
		prefix := bits - sizeBits
		out = append(out, Cidr{Addr: new(big.Int).Set(cur), Prefix: prefix, Bits: bits})
		step := new(big.Int).Lsh(big.NewInt(1), uint(sizeBits))
		cur = new(big.Int).Add(cur, step)
	}
	return out
}

// bigIntToIP converts a BigInteger address back to net.IP for
// human-readable rendering.
func bigIntToIP(addr *big.Int, bits int) net.IP {
	byteCount := bits / 8
	raw := addr.Bytes()
	padded := make([]byte, byteCount)
	if len(raw) <= byteCount {
		copy(padded[byteCount-len(raw):], raw)
	} else {
		copy(padded, raw[len(raw)-byteCount:])
	}
	return net.IP(padded)
}

// PrivateNetworks: standard "private network" CIDRs the user can
// exclude with one toggle. RFC1918 (v4) + IPv6 ULA + link-local on
// both families. Multicast is NOT included.
var PrivateNetworks = func() []Cidr {
	specs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"fc00::/7",
		"fe80::/10",
	}
	out := make([]Cidr, 0, len(specs))
	for _, s := range specs {
		c, err := ParseCidr(s)
		if err == nil {
			out = append(out, c)
		}
	}
	return out
}()
