package gardener

import (
	"io"
	"net/url"
	"time"

	"github.com/cloudfoundry-incubator/garden"
	"github.com/cloudfoundry-incubator/garden-shed/rootfs_provider"
	"github.com/pivotal-golang/lager"
)

//go:generate counterfeiter . SysInfoProvider

type SysInfoProvider interface {
	TotalMemory() (uint64, error)
	TotalDisk() (uint64, error)
}

//go:generate counterfeiter . Containerizer
//go:generate counterfeiter . Networker
//go:generate counterfeiter . VolumeCreator
//go:generate counterfeiter . UidGenerator

type Containerizer interface {
	Create(log lager.Logger, spec DesiredContainerSpec) error
	StreamIn(log lager.Logger, handle string, spec garden.StreamInSpec) error
	StreamOut(log lager.Logger, handle string, spec garden.StreamOutSpec) (io.ReadCloser, error)
	Run(log lager.Logger, handle string, spec garden.ProcessSpec, io garden.ProcessIO) (garden.Process, error)
	Destroy(log lager.Logger, handle string) error
	Handles() ([]string, error)
}

type Networker interface {
	Hook(log lager.Logger, handle, spec string) (Hook, error)
	Capacity() uint64
	Destroy(log lager.Logger, handle string) error
	NetIn(handle string, hostPort, containerPort uint32) (uint32, uint32, error)
	NetOut(log lager.Logger, handle string, rule garden.NetOutRule) error
}

type VolumeCreator interface {
	Create(log lager.Logger, handle string, spec rootfs_provider.Spec) (string, []string, error)
	Destroy(log lager.Logger, handle string) error
}

type UidGenerator interface {
	Generate() string
}

//go:generate counterfeiter . PropertyManager

type PropertyManager interface {
	All(handle string) (props garden.Properties, err error)
	Set(handle string, name string, value string)
	Remove(handle string, name string) error
	Get(handle string, name string) (string, error)
	MatchesAll(handle string, props garden.Properties) bool
	DestroyKeySpace(string) error
}

type Starter interface {
	Start() error
}

type UidGeneratorFunc func() string

func (fn UidGeneratorFunc) Generate() string {
	return fn()
}

type Hook struct {
	Path string
	Args []string
}

type DesiredContainerSpec struct {
	Handle string

	// Path to the Root Filesystem for the container
	RootFSPath string

	// Network pre-start hook
	NetworkHook Hook

	// Bind mounts
	BindMounts []garden.BindMount

	// Container is privileged
	Privileged bool
}

// Gardener orchestrates other components to implement the Garden API
type Gardener struct {
	// SysInfoProvider returns total memory and total disk
	SysInfoProvider SysInfoProvider

	// Containerizer runs and manages linux containers
	Containerizer Containerizer

	// UidGenerator generates unique ids for containers
	UidGenerator UidGenerator

	// Starter runs any needed start-up tasks (e.g. setting up cgroups)
	Starter

	// Networker creates a network for containers
	Networker Networker

	// VolumeCreator creates volumes for containers
	VolumeCreator VolumeCreator

	Logger lager.Logger

	// PropertyManager creates map of container properties
	PropertyManager PropertyManager
}

func (g *Gardener) Create(spec garden.ContainerSpec) (garden.Container, error) {
	log := g.Logger.Session("create")

	if spec.Handle == "" {
		spec.Handle = g.UidGenerator.Generate()
	}

	hook, err := g.Networker.Hook(log, spec.Handle, spec.Network)
	if err != nil {
		return nil, err
	}

	rootFSURL, err := url.Parse(spec.RootFSPath)
	if err != nil {
		g.Networker.Destroy(g.Logger, spec.Handle)
		return nil, err
	}

	rootFSPath, _, err := g.VolumeCreator.Create(log, spec.Handle, rootfs_provider.Spec{
		RootFS:     rootFSURL,
		QuotaSize:  int64(spec.Limits.Disk.ByteHard),
		QuotaScope: rootfs_provider.QuotaScopeExclusive,
	})
	if err != nil {
		g.Networker.Destroy(g.Logger, spec.Handle)
		return nil, err
	}

	if err := g.Containerizer.Create(log, DesiredContainerSpec{
		Handle:      spec.Handle,
		RootFSPath:  rootFSPath,
		NetworkHook: hook,
		Privileged:  spec.Privileged,
		BindMounts:  spec.BindMounts,
	}); err != nil {
		g.Networker.Destroy(g.Logger, spec.Handle)
		return nil, err
	}

	container, err := g.Lookup(spec.Handle)
	if err != nil {
		return nil, err
	}

	for name, value := range spec.Properties {
		err := container.SetProperty(name, value)
		if err != nil {
			return nil, err
		}
	}

	return container, nil
}

func (g *Gardener) Lookup(handle string) (garden.Container, error) {
	return &container{
		logger:          g.Logger,
		handle:          handle,
		containerizer:   g.Containerizer,
		networker:       g.Networker,
		propertyManager: g.PropertyManager,
	}, nil
}

func (g *Gardener) Destroy(handle string) error {
	if err := g.Containerizer.Destroy(g.Logger, handle); err != nil {
		return err
	}

	if err := g.Networker.Destroy(g.Logger, handle); err != nil {
		return err
	}

	if err := g.VolumeCreator.Destroy(g.Logger, handle); err != nil {
		return err
	}

	return g.PropertyManager.DestroyKeySpace(handle)
}

func (g *Gardener) Stop()                                    {}
func (g *Gardener) GraceTime(garden.Container) time.Duration { return 0 }
func (g *Gardener) Ping() error                              { return nil }

func (g *Gardener) Capacity() (garden.Capacity, error) {
	mem, err := g.SysInfoProvider.TotalMemory()
	if err != nil {
		return garden.Capacity{}, err
	}

	disk, err := g.SysInfoProvider.TotalDisk()
	if err != nil {
		return garden.Capacity{}, err
	}

	cap := g.Networker.Capacity()

	return garden.Capacity{
		MemoryInBytes: mem,
		DiskInBytes:   disk,
		MaxContainers: cap,
	}, nil
}

func (g *Gardener) Containers(props garden.Properties) ([]garden.Container, error) {
	log := g.Logger.Session("list-containers")

	log.Info("starting")
	defer log.Info("finished")

	handles, err := g.Containerizer.Handles()
	if err != nil {
		log.Error("handles-failed", err)
		return []garden.Container{}, err
	}

	var containers []garden.Container
	for _, handle := range handles {
		if g.PropertyManager.MatchesAll(handle, props) {
			container, err := g.Lookup(handle)
			if err != nil {
				log.Error("lookup-failed", err)
			}

			containers = append(containers, container)
		}
	}

	return containers, nil
}

func (g *Gardener) BulkInfo(handles []string) (map[string]garden.ContainerInfoEntry, error) {
	return nil, nil
}

func (g *Gardener) BulkMetrics(handles []string) (map[string]garden.ContainerMetricsEntry, error) {
	return nil, nil
}
