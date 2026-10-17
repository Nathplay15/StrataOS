package antivirus

import (
	"os"
	"syscall"
)

func sysStat(fi os.FileInfo) ([2]int, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return [2]int{int(st.Uid), int(st.Gid)}, true
	}
	return [2]int{}, false
}
