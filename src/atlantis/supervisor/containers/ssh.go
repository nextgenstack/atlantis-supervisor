package containers

import (
	"atlantis/supervisor/rpc/types"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

type SSHCmd []string

func pretending() bool {
	return os.Getenv("SUPERVISOR_PRETEND") != ""
}

func (s SSHCmd) Execute() error {
	if pretending() {
		log.Printf("[pretend] ssh %s", strings.Join(s, " "))
		return nil
	}
	log.Printf("ssh %s", strings.Join(s, " "))
	cmd := exec.Command("ssh", s...)
	output, err := cmd.CombinedOutput()
	log.Printf("-> %s", output)
	if err != nil {
		log.Println("-> Error:", err)
	}
	return err
}

func AuthorizeSSHUser(c types.GenericContainer, user, publicKey string) error {
	// copy file to container
	// rebuild authorize_keys
	return SSHCmd{"-p", fmt.Sprintf("%d", c.GetSSHPort()), "-i", "/opt/atlantis/supervisor/master_id_rsa", "-o",
		"UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "root@localhost",
		fmt.Sprintf("echo \"%s\" >/root/.ssh/authorized_keys.d/%s.pub && rebuild_authorized_keys", publicKey,
			user)}.Execute()
}

func DeauthorizeSSHUser(c types.GenericContainer, user string) error {
	// delete file from container
	// rebuild authorize_keys
	return SSHCmd{"-p", fmt.Sprintf("%d", c.GetSSHPort()), "-i", "/opt/atlantis/supervisor/master_id_rsa", "-o",
		"UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "root@localhost",
		fmt.Sprintf("rm /root/.ssh/authorized_keys.d/%s.pub && rebuild_authorized_keys",
			user)}.Execute()
}

func SetMaintenance(c types.GenericContainer, maint bool) error {
	if maint {
		// touch /etc/maint
		return SSHCmd{"-p", fmt.Sprintf("%d", c.GetSSHPort()), "-i", "/opt/atlantis/supervisor/master_id_rsa", "-o",
			"UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "root@localhost",
			"touch /etc/maint"}.Execute()
	}
	// rm -f /etc/maint
	return SSHCmd{"-p", fmt.Sprintf("%d", c.GetSSHPort()), "-i", "/opt/atlantis/supervisor/master_id_rsa", "-o",
		"UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "root@localhost",
		"rm -f /etc/maint"}.Execute()
}
