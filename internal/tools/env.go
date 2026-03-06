package tools

type ENV string

const (
	ENV_LOCAL ENV = "local"
	ENV_SSH   ENV = "ssh"
)

type Env struct {
	env      ENV
	pwd      string
	platform string
}

func NewEnv(pwd, platform string) *Env {
	return &Env{
		env:      ENV_LOCAL,
		pwd:      pwd,
		platform: platform,
	}
}
