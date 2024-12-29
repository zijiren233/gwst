package ws

import "unsafe"

// The change of bytes will cause the change of string synchronously
func bytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// If string is readonly, modifying bytes will cause panic
func stringToBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
