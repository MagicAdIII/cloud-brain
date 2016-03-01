package cloudbrain

import (
	"github.com/Sirupsen/logrus"
	"github.com/travis-ci/cloud-brain/cbcontext"
	"github.com/travis-ci/cloud-brain/cloud"
	"github.com/travis-ci/cloud-brain/database"
	"github.com/travis-ci/cloud-brain/worker"
	"golang.org/x/net/context"
)

const MaxCreateRetries = 10

type Core struct {
	cloud cloud.Provider
	db    database.DB
	wb    worker.Backend
}

// TODO(henrikhodne): Is this necessary? Why not just make a Core directly?
type CoreConfig struct {
	CloudProvider cloud.Provider
	DB            database.DB
	WorkerBackend worker.Backend
}

func NewCore(conf *CoreConfig) *Core {
	c := &Core{
		cloud: conf.CloudProvider,
		db:    conf.DB,
		wb:    conf.WorkerBackend,
	}

	return c
}

// GetInstance gets the instance information stored in the database for a given
// instance ID.
func (c *Core) GetInstance(ctx context.Context, id string) (*Instance, error) {
	instance, err := c.db.GetInstance(id)

	if err == database.ErrInstanceNotFound {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &Instance{
		ID:           instance.ID,
		ProviderName: instance.Provider,
		Image:        instance.Image,
		State:        instance.State,
		IPAddress:    instance.IPAddress,
	}, nil
}

// CreateInstance creates an instance in the database and queues off the cloud
// create job in the background.
func (c *Core) CreateInstance(ctx context.Context, providerName, imageName string) (*Instance, error) {
	id, err := c.db.CreateInstance(database.Instance{
		Provider: providerName,
		Image:    imageName,
		State:    "creating",
	})
	if err != nil {
		cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
			"err":        err,
			"provider":   providerName,
			"image_name": imageName,
		}).Error("error creating instance in database")
		return nil, err
	}

	err = c.wb.Enqueue(worker.Job{
		Context:    ctx,
		Payload:    []byte(id),
		Queue:      "create",
		MaxRetries: MaxCreateRetries,
	})
	if err != nil {
		cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
			"err":         err,
			"instance_id": id,
		}).Error("error enqueueing 'create' job in the background")
		// TODO(henrikhodne): Delete the record in the database?
		return nil, err
	}

	return &Instance{
		ID:           id,
		ProviderName: providerName,
		Image:        imageName,
		State:        "creating",
	}, nil
}

func (c *Core) ProviderCreateInstance(ctx context.Context, byteID []byte) error {
	id := string(byteID)

	cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
		"instance_id": id,
	}).Info("creating instance")

	dbInstance, err := c.db.GetInstance(id)
	if err != nil {
		cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
			"err":         err,
			"instance_id": id,
		}).Error("error fetching instance from DB")
		return err
	}

	instance, err := c.cloud.Create(dbInstance.Image)
	if err != nil {
		cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
			"err":         err,
			"instance_id": id,
		}).Error("error creating instance")
		return err
	}

	dbInstance.ProviderID = instance.ID
	dbInstance.State = "starting"

	err = c.db.UpdateInstance(dbInstance)
	if err != nil {
		cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
			"err":         err,
			"instance_id": id,
			"provider_id": instance.ID,
		}).Error("couldn't update instance in DB")
		return err
	}

	cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
		"instance_id": id,
		"provider_id": instance.ID,
	}).Info("created instance")

	return nil
}

func (c *Core) ProviderRefresh(ctx context.Context) error {
	instances, err := c.cloud.List()
	if err != nil {
		return err
	}

	for _, instance := range instances {
		// TODO(henrikhodne): What to do with ProviderName here? (And in the
		// rest of the function).
		dbInstance, err := c.db.GetInstanceByProviderID("fake", instance.ID)
		if err != nil {
			cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
				"provider":    "fake",
				"provider_id": instance.ID,
			}).Error("failed fetching instance from database")
			continue
		}

		dbInstance.IPAddress = instance.IPAddress
		dbInstance.State = string(instance.State)

		err = c.db.UpdateInstance(dbInstance)
		if err != nil {
			cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
				"provider":    "fake",
				"provider_id": instance.ID,
				"db_id":       dbInstance.ID,
			}).Error("failed to update instance in database")
		}
	}

	cbcontext.LoggerFromContext(ctx).WithFields(logrus.Fields{
		"provider":       "fake",
		"instance_count": len(instances),
	}).Info("refreshed instances")

	return nil
}

type Instance struct {
	ID           string
	ProviderName string
	Image        string
	State        string
	IPAddress    string
}