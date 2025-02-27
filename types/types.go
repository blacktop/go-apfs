package types

import (
	"encoding/binary"
	"fmt"

	"github.com/fatih/color"
)

const FSROOT_OID = 2

var BLOCK_SIZE uint64

type paddr_t int64

type prange struct {
	StartPaddr paddr_t
	BlockCount uint64
}

type magic [4]byte

func (m magic) String() string {
	return string(m[:])
}

// UUID is a uuid object
type UUID [16]byte

// IsNull returns true if UUID is 00000000-0000-0000-0000-000000000000
func (u UUID) IsNull() bool {
	return u == [16]byte{0}
}

func (u UUID) String() string {
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		u[0], u[1], u[2], u[3], u[4], u[5], u[6], u[7], u[8], u[9], u[10], u[11], u[12], u[13], u[14], u[15])
}

type uid_t uint32
type gid_t uint32

func (u uid_t) String() string {
	switch u {
	case 0:
		return "root"
	case 1:
		return "daemon"
	case 501:
		return "mobile"
	default:
		return fmt.Sprintf("%d", u)
	}
}

func (g gid_t) String() string {
	switch g {
	case 0:
		return "wheel"
	case 1:
		return "daemon"
	case 2:
		return "kmem"
	case 3:
		return "sys"
	case 4:
		return "tty"
	case 5:
		return "operator"
	case 6:
		return "mail"
	case 7:
		return "bin"
	case 8:
		return "procview"
	case 9:
		return "procmod"
	case 10:
		return "owner"
	case 12:
		return "everyone"
	case 16:
		return "group"
	case 20:
		return "staff"
	case 29:
		return "certusers"
	case 50:
		return "authedusers"
	case 51:
		return "interactusers"
	case 52:
		return "netusers"
	case 53:
		return "consoleusers"
	case 61:
		return "localaccounts"
	case 62:
		return "netaccounts"
	case 68:
		return "dialer"
	case 69:
		return "network"
	case 80:
		return "admin"
	case 90:
		return "accessibility"
	case 299:
		return "systemusers"
	case 501:
		return "mobile"
	default:
		return fmt.Sprintf("%d", g)
	}
}

func CreateChecksum(data []byte) uint64 {
	var sum1, sum2 uint64

	modValue := uint64(2<<31 - 1)

	for i := range len(data) / 4 {
		d := binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
		sum1 = (sum1 + uint64(d)) % modValue
		sum2 = (sum2 + sum1) % modValue
	}

	check1 := modValue - ((sum1 + sum2) % modValue)
	check2 := modValue - ((sum1 + check1) % modValue)

	return (check2 << 32) | check1
}

func VerifyChecksum(data []byte) bool {
	var sum1, sum2 uint64

	modValue := uint64(2<<31 - 1)

	for i := range len(data) / 4 {
		d := binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
		sum1 = (sum1 + uint64(d)) % modValue
		sum2 = (sum2 + sum1) % modValue
	}

	return (sum2<<32)|sum1 != 0
}

var NameColor = color.New(color.Bold, color.FgHiBlue).SprintFunc()
var DirColor = color.New(color.Bold, color.FgHiBlue).SprintFunc()
var TypeColor = color.New(color.Bold, color.FgHiYellow).SprintFunc()
var timeColor = color.New(color.FgCyan).SprintFunc()
var hexdumpColor = color.New(color.Faint, color.FgHiWhite).SprintFunc()
