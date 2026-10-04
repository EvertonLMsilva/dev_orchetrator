package infrastructure

import "context"

// obtain transfers ownership of the sensitive buffer to the environment. The
// source must not retain or log it. No production credential reader is provided.
type chatGPTAuthSource interface {
	obtain(context.Context) ([]byte, error)
}

// prepareAuth writes a mode 0600 file only to the configured container tmpfs,
// before Start; no host staging file, bind mount or persistent volume is allowed.
// Implementations must not persist, log or retain material. Remove destroys
// container storage, including after partial preparation failures.
type chatGPTAuthDocker interface {
	DockerLifecycle
	prepareAuth(context.Context, string, string, []byte) error
}

func newAuthenticatedDockerEnvironment(docker chatGPTAuthDocker, source chatGPTAuthSource) *DockerExecutionEnvironment {
	return &DockerExecutionEnvironment{docker: docker, authRequired: true, authSource: source}
}
