package tools

import (
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"
)

// BuildSSHAuthMethods builds a list of SSH auth methods by checking the
// SSH agent socket and common private key files in ~/.ssh/.
func BuildSSHAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	// Try SSH agent (only if it actually holds keys)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			ag := sshagent.NewClient(conn)
			if keys, err := ag.List(); err == nil && len(keys) > 0 {
				methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
			}
		}
	}

	// Try common key files
	keyPaths := []string{
		os.Getenv("HOME") + "/.ssh/id_rsa",
		os.Getenv("HOME") + "/.ssh/id_ed25519",
		os.Getenv("HOME") + "/.ssh/id_ecdsa",
	}
	for _, keyPath := range keyPaths {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	return methods
}
