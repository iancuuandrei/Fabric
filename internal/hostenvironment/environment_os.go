package hostenvironment

import "os"

func currentEnvironment() []string {
	return os.Environ()
}
