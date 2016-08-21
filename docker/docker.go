package docker

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dockerAPI "github.com/docker/engine-api/client"
	dockerTypes "github.com/docker/engine-api/types"
	dockerEvents "github.com/docker/engine-api/types/events"
	"github.com/docker/engine-api/types/filters"
	"golang.org/x/net/context"
)

const (
	//DefaultDockerHost is used as a default docker host to connect to
	//if no other value is given.
	DefaultDockerHost = "unix:///var/run/docker.sock"

	//timeout in seconds for docker operations
	defaultOperationTimeout = 10 * time.Second
	//container operations timeout, docker api interprets the value as seconds
	containerOpTimeout = 10
)

//Defaults for listing images
var defaultImageListOptions = dockerTypes.ImageListOptions{
	All: false}

//DockerDaemon knows how to talk to the Docker daemon
type DockerDaemon struct {
	client        dockerAPI.APIClient              //client used to to connect to the Docker daemon
	containerByID map[string]dockerTypes.Container // Containers by their id
	containers    []dockerTypes.Container
	images        []dockerTypes.Image
	networks      []dockerTypes.NetworkResource
	err           error // Errors, if any.
	connected     bool
	dockerEnv     *DockerEnv
	version       *dockerTypes.Version
	refreshLock   sync.Mutex
	eventLog      *EventLog
}

//Containers returns the containers known by the daemon
func (daemon *DockerDaemon) Containers() []dockerTypes.Container {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	return daemon.containers
}

//ContainerAt returns the container at the given position
func (daemon *DockerDaemon) ContainerAt(pos int) (dockerTypes.Container, error) {
	id, _, err := daemon.ContainerIDAt(pos)
	if err == nil {
		return daemon.ContainerByID(id), nil
	}
	return dockerTypes.Container{}, err
}

//ContainersCount returns the number of containers found.
func (daemon *DockerDaemon) ContainersCount() int {
	return len(daemon.containers)
}

//ContainerIDAt returns the container ID of the container found at the given
//position.
func (daemon *DockerDaemon) ContainerIDAt(pos int) (string, string, error) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	if pos < 0 || pos >= len(daemon.containers) {
		return "", "", fmt.Errorf("Invalid container position: %d", pos)
	}
	return daemon.containers[pos].ID, TruncateID(daemon.containers[pos].ID), nil
}

//ContainerByID returns the container with the given ID
func (daemon *DockerDaemon) ContainerByID(cid string) dockerTypes.Container {
	return daemon.containerByID[cid]
}

//DockerEnv returns Docker-related environment variables
func (daemon *DockerDaemon) DockerEnv() *DockerEnv {
	return daemon.dockerEnv
}

// Events returns a channel to receive Docker events.
func (daemon *DockerDaemon) Events() (<-chan dockerEvents.Message, chan<- struct{}, error) {

	options := dockerTypes.EventsOptions{
	//Since: time.Now().String(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := daemon.client.Events(ctx, options)

	if err != nil {
		return nil, nil, err
	}
	eventC := make(chan dockerEvents.Message)
	done := make(chan struct{})

	go func() {
		decodeEvents(
			ctx,
			events,
			streamEvents(eventC),
			logEvents(daemon.eventLog))
		<-done
		cancel()
		close(eventC)
		events.Close()
	}()

	return eventC, done, nil
}

//EventLog returns the events log
func (daemon *DockerDaemon) EventLog() *EventLog {
	return daemon.eventLog
}

//History returns image history
func (daemon *DockerDaemon) History(id string) ([]dockerTypes.ImageHistory, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ImageHistory(
		ctx, id)
}

//ImageAt returns the Image found at the given
//position.
func (daemon *DockerDaemon) ImageAt(pos int) (*dockerTypes.Image, error) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	if pos >= len(daemon.images) {
		return nil, errors.New("Position is higher than number of images")
	}
	return &daemon.images[pos], nil
}

//Images returns the list of Docker images
func (daemon *DockerDaemon) Images() ([]dockerTypes.Image, error) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	return daemon.images, nil
}

//ImagesCount returns the number of images
func (daemon *DockerDaemon) ImagesCount() int {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	return len(daemon.images)
}

//Info returns system-wide information about the Docker server.
func (daemon *DockerDaemon) Info() (dockerTypes.Info, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.Info(ctx)
}

//Inspect the container with the given id
func (daemon *DockerDaemon) Inspect(id string) (dockerTypes.ContainerJSON, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ContainerInspect(ctx, id)
}

//InspectImage the image with the name
func (daemon *DockerDaemon) InspectImage(name string) (dockerTypes.ImageInspect, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	inspect, _, err := daemon.client.ImageInspectWithRaw(ctx, name, true)
	return inspect, err
}

//IsContainerRunning returns true if the container with the given  is running
func (daemon *DockerDaemon) IsContainerRunning(id string) bool {
	return IsContainerRunning(daemon.ContainerByID(id))
}

//Kill the container with the given id
func (daemon *DockerDaemon) Kill(id string) error {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	//TODO Sends the right signal

	return daemon.client.ContainerKill(ctx, id, "")
}

//Logs shows the logs of the container with the given id
func (daemon *DockerDaemon) Logs(id string) io.ReadCloser {
	options := dockerTypes.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: false,
		Follow:     true,
		Details:    false,
	}
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	reader, _ := daemon.client.ContainerLogs(ctx, id, options)
	return reader
}

//Networks returns the list of Docker networks
func (daemon *DockerDaemon) Networks() ([]dockerTypes.NetworkResource, error) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	return daemon.networks, nil
}

//NetworkAt returns the network found at the given position.
func (daemon *DockerDaemon) NetworkAt(pos int) (*dockerTypes.NetworkResource, error) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	if pos >= len(daemon.networks) {
		return nil, errors.New("Position is higher than number of networks")
	}
	return &daemon.networks[pos], nil
}

//NetworksCount returns the number of networks reported by Docker
func (daemon *DockerDaemon) NetworksCount() int {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	return len(daemon.networks)
}

//NetworkInspect returns network detailed information
func (daemon *DockerDaemon) NetworkInspect(id string) (dockerTypes.NetworkResource, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.NetworkInspect(
		ctx, id)
}

//Ok is true if connecting to the Docker daemon went fine
func (daemon *DockerDaemon) Ok() (bool, error) {
	return daemon.err == nil, daemon.err
}

//RestartContainer restarts the container with the given id
func (daemon *DockerDaemon) RestartContainer(id string) error {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	//fixme: timeout to start a container
	return daemon.client.ContainerRestart(ctx, id, containerOpTimeout)
}

//Rm removes the container with the given id
func (daemon *DockerDaemon) Rm(id string) error {
	opts := dockerTypes.ContainerRemoveOptions{
		RemoveVolumes: false,
		RemoveLinks:   false,
		Force:         true,
	}
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ContainerRemove(ctx, id, opts)
}

//Refresh the container list
func (daemon *DockerDaemon) Refresh(allContainers bool) error {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()

	containers, containerByID, err := containers(daemon.client, allContainers)

	if err == nil {
		daemon.containerByID = containerByID
		daemon.containers = containers
	}
	return err
}

//RefreshImages refreshes the image list
func (daemon *DockerDaemon) RefreshImages() error {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()

	images, err := images(daemon.client, defaultImageListOptions)

	if err == nil {
		daemon.images = images
	}
	return err
}

//RefreshNetworks refreshes the network list
func (daemon *DockerDaemon) RefreshNetworks() error {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()

	networks, err := networks(daemon.client)

	if err == nil {
		daemon.networks = networks
	}
	return err
}

//RemoveAllStoppedContainers removes all stopped containers
func (daemon *DockerDaemon) RemoveAllStoppedContainers() (int, error) {
	containers, _, err := containers(daemon.client, true)
	var count uint32
	errs := make(chan error, 1)
	defer close(errs)
	if err == nil {
		var wg sync.WaitGroup
		for _, container := range containers {
			if !IsContainerRunning(container) {
				wg.Add(1)
				go func(id string) {
					defer atomic.AddUint32(&count, 1)
					defer wg.Done()
					err = daemon.Rm(id)
					if err != nil {
						select {
						case errs <- err:
						default:
						}
					}
				}(container.ID)
			}
		}
		wg.Wait()
		select {
		case e := <-errs:
			return 0, e
		default:
		}
	}
	return int(atomic.LoadUint32(&count)), err
}

//RemoveDanglingImages removes dangling images
func (daemon *DockerDaemon) RemoveDanglingImages() (int, error) {
	danglingfilters := filters.NewArgs()
	danglingfilters.Add("dangling", "true")
	images, err := images(daemon.client,
		dockerTypes.ImageListOptions{
			Filters: danglingfilters})
	var count uint32
	errs := make(chan error, 1)
	defer close(errs)
	if err == nil {
		var wg sync.WaitGroup
		for _, image := range images {
			wg.Add(1)
			go func(id string) {
				defer atomic.AddUint32(&count, 1)
				defer wg.Done()
				_, err = daemon.Rmi(id, true)
				if err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}(image.ID)
		}
		wg.Wait()
		select {
		case e := <-errs:
			return 0, e
		default:
		}
	}
	return int(atomic.LoadUint32(&count)), err
}

//RemoveNetwork removes the network with the given id
func (daemon *DockerDaemon) RemoveNetwork(id string) error {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.NetworkRemove(ctx, id)
}

//Rmi removes the image with the given name
func (daemon *DockerDaemon) Rmi(name string, force bool) ([]dockerTypes.ImageDelete, error) {
	options := dockerTypes.ImageRemoveOptions{
		Force: force,
	}
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ImageRemove(ctx, name, options)
}

//Stats shows resource usage statistics of the container with the given id
func (daemon *DockerDaemon) Stats(id string) (<-chan *Stats, chan<- struct{}) {
	return StatsChannel(daemon, daemon.ContainerByID(id), true)
}

//StopContainer stops the container with the given id
func (daemon *DockerDaemon) StopContainer(id string) error {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ContainerStop(ctx, id, containerOpTimeout)
}

//Sort the list of containers by the given mode
func (daemon *DockerDaemon) Sort(sortMode SortMode) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	SortContainers(daemon.containers, sortMode)
}

//SortImages sorts the list of images by the given mode
func (daemon *DockerDaemon) SortImages(sortMode SortImagesMode) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	SortImages(daemon.images, sortMode)
}

//SortNetworks sortes the list of networks by the given mode
func (daemon *DockerDaemon) SortNetworks(sortMode SortNetworksMode) {
	daemon.refreshLock.Lock()
	defer daemon.refreshLock.Unlock()
	SortNetworks(daemon.networks, sortMode)
}

//Top returns Top information for the given container
func (daemon *DockerDaemon) Top(id string) (dockerTypes.ContainerProcessList, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return daemon.client.ContainerTop(ctx, id, nil)
}

//Version returns  version information about the Docker Engine
func (daemon *DockerDaemon) Version() (*dockerTypes.Version, error) {
	if daemon.version == nil {
		//TODO use cancel function
		ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

		v, err := daemon.client.ServerVersion(ctx)
		if err == nil {
			daemon.version = &v
			return daemon.version, nil
		}
		return nil, err
	}
	return daemon.version, nil
}

func containers(client dockerAPI.APIClient, allContainers bool) ([]dockerTypes.Container, map[string]dockerTypes.Container, error) {
	//TODO use cancel function
	//Since this is how the dry fist connects to the Docker daemon
	//a different (longer) timeout is used.
	ctx, _ := context.WithTimeout(context.Background(), DefaultConnectionTimeout)

	containers, err := client.ContainerList(ctx, dockerTypes.ContainerListOptions{All: allContainers, Size: true})
	if err == nil {
		var cmap = make(map[string]dockerTypes.Container)

		for _, c := range containers {
			cmap[c.ID] = c
		}
		return containers, cmap, nil
	}
	return nil, nil, err
}

func images(client dockerAPI.APIClient, opts dockerTypes.ImageListOptions) ([]dockerTypes.Image, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return client.ImageList(ctx, opts)
}

func networks(client dockerAPI.APIClient) ([]dockerTypes.NetworkResource, error) {
	//TODO use cancel function
	ctx, _ := context.WithTimeout(context.Background(), defaultOperationTimeout)

	return client.NetworkList(ctx, dockerTypes.NetworkListOptions{})
}

//GetBool returns false if the given string looks like you mean
//false, true otherwise. Func does not belong here.
func GetBool(key string) (value bool) {
	s := strings.ToLower(strings.Trim(key, " "))
	if s == "" || s == "0" || s == "no" || s == "false" || s == "none" {
		return false
	}
	return true
}

//IsContainerRunning returns true if the given container is running
func IsContainerRunning(container dockerTypes.Container) bool {
	return strings.Contains(container.Status, "Up")
}
