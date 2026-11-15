//go:build darwin && cgo

package qualify

/*
#include <libproc.h>
#include <sys/resource.h>

static long long lifetime_max(int pid) {
	struct rusage_info_v4 ri;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V4, (rusage_info_t *)&ri) != 0) {
		return -1;
	}
	return (long long)ri.ri_lifetime_max_phys_footprint;
}
*/
import "C"

// peakRSS returns a running process's own peak memory in bytes and the method (F3): on macOS
// the lifetime maximum physical footprint from proc_pid_rusage, excluding its children.
func peakRSS(pid int) (int64, string) {
	return int64(C.lifetime_max(C.int(pid))), "proc_pid_rusage RUSAGE_INFO_V4 ri_lifetime_max_phys_footprint, process only"
}
