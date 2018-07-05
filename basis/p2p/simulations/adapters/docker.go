//
// (at your option) any later version.
//
//

package adapters

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/docker/docker/pkg/reexec"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/basis/p2p/discover"
)

var (
	ErrLinuxOnly = errors.New("DockerAdapter can only be used on Linux as it uses the current binary (which must be a Linux binary)")
)

//
type DockerAdapter struct {
	ExecAdapter
}

func NewDockerAdapter() (*DockerAdapter, error) {
	// Since Docker containers run on Linux and this adapter runs the
	// current binary in the container, it must be compiled for Linux.
	//
	// It is reasonable to require this because the caller can just
	// compile the current binary in a Docker container.
	if runtime.GOOS != "linux" {
		return nil, ErrLinuxOnly
	}

	if err := buildDockerImage(); err != nil {
		return nil, err
	}

	return &DockerAdapter{
		ExecAdapter{
			nodes: make(map[discover.NodeID]*ExecNode),
		},
	}, nil
}

func (d *DockerAdapter) Name() string {
	return "docker-adapter"
}

func (d *DockerAdapter) NewNode(config *NodeConfig) (Node, error) {
	if len(config.Services) == 0 {
		return nil, errors.New("node must have at least one service")
	}
	for _, service := range config.Services {
		if _, exists := serviceFuncs[service]; !exists {
			return nil, fmt.Errorf("unknown node service %q", service)
		}
	}

	// generate the config
	conf := &execNodeConfig{
		Stack: node.DefaultConfig,
		Node:  config,
	}
	conf.Stack.DataDir = "/data"
	conf.Stack.WSHost = "0.0.0.0"
	conf.Stack.WSOrigins = []string{"*"}
	conf.Stack.WSExposeAll = true
	conf.Stack.P2P.EnableMsgEvents = false
	conf.Stack.P2P.NoDiscovery = true
	conf.Stack.P2P.NAT = nil
	conf.Stack.NoUSB = true

	// listen on all interfaces on a given port, which we set when we
	// initialise NodeConfig (usually a random port)
	conf.Stack.P2P.ListenAddr = fmt.Sprintf(":%d", config.Port)

	node := &DockerNode{
		ExecNode: ExecNode{
			ID:      config.ID,
			Config:  conf,
			adapter: &d.ExecAdapter,
		},
	}
	node.newCmd = node.dockerCommand
	d.ExecAdapter.nodes[node.ID] = &node.ExecNode
	return node, nil
}

type DockerNode struct {
	ExecNode
}

//
func (n *DockerNode) dockerCommand() *exec.Cmd {
	return exec.Command(
		"sh", "-c",
		fmt.Sprintf(
			`exec docker run --interactive --env _P2P_NODE_CONFIG="${_P2P_NODE_CONFIG}" %s p2p-node %s %s`,
			dockerImage, strings.Join(n.Config.Node.Services, ","), n.ID.String(),
		),
	)
}

const dockerImage = "p2p-node"

//
func buildDockerImage() error {
	// create a directory to use as the build context
	dir, err := ioutil.TempDir("", "p2p-docker")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// copy the current binary into the build context
	bin, err := os.Open(reexec.Self())
	if err != nil {
		return err
	}
	defer bin.Close()
	dst, err := os.OpenFile(filepath.Join(dir, "self.bin"), os.O_WRONLY|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, bin); err != nil {
		return err
	}

	// create the Dockerfile
	dockerfile := []byte(`
FROM ubuntu:16.04
RUN mkdir /data
ADD self.bin /bin/p2p-node
	`)
	if err := ioutil.WriteFile(filepath.Join(dir, "Dockerfile"), dockerfile, 0644); err != nil {
		return err
	}

	// run 'docker build'
	cmd := exec.Command("docker", "build", "-t", dockerImage, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error building docker image: %s", err)
	}

	return nil
}
