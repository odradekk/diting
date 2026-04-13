package doctor

import "os/exec"

// lookPath is the real implementation of Environment.LookPath. It lives
// in its own file so that unit tests can fully stub the LookPath field
// with a closure and never pull in os/exec at all.
func lookPath(file string) (string, error) {
	return exec.LookPath(file)
}
