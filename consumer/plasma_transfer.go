package consumer

import (
	"bytes"
	"fmt"
	"os"

	"github.com/couchbase/eventing/logging"
	"github.com/couchbase/plasma"
)

// CreateTempPlasmaStore preps up temporary plasma file on disk, housing the contents for supplied
// vbucket. Will be called during the course of rebalance
func (c *Consumer) CreateTempPlasmaStore(vb uint16) error {
	r := c.vbPlasmaStore.NewReader()
	w := c.vbPlasmaStore.NewWriter()
	snapshot := c.vbPlasmaStore.NewSnapshot()
	defer snapshot.Close()

	itr, err := r.NewSnapshotIterator(snapshot)
	if err != nil {
		logging.Errorf("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v Failed to create snapshot, err: %v",
			c.workerName, c.Pid(), vb, err)
		return err
	}

	vbPlasmaDir := fmt.Sprintf("%v/reb_%v_%v_timer.data", c.eventingDir, vb, c.app.AppName)

	vbRebPlasmaStore, err := c.openPlasmaStore(vbPlasmaDir)
	if err != nil {
		logging.Errorf("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v Failed to create temporary plasma instance during rebalance, err: %v",
			c.workerName, c.Pid(), vb, err)
		return err
	}

	logging.Infof("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v tempPlasmaDir: %v created temp plasma instance during rebalance",
		c.workerName, c.Pid(), vb, vbPlasmaDir)

	defer vbRebPlasmaStore.Close()
	defer vbRebPlasmaStore.PersistAll()

	rebPlasmaWriter := vbRebPlasmaStore.NewWriter()

	for itr.SeekFirst(); itr.Valid(); itr.Next() {
		keyPrefix := []byte(fmt.Sprintf("vb_%v::", vb))

		if bytes.Compare(itr.Key(), keyPrefix) > 0 {
			val, err := w.LookupKV(itr.Key())
			if err != nil && err != plasma.ErrItemNoValue {
				logging.Tracef("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v key: %s failed to lookup, err: %v",
					c.workerName, c.Pid(), vb, string(itr.Key()), err)
				continue
			}
			logging.Tracef("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v read key: %s from source plasma store",
				c.workerName, c.Pid(), vb, string(itr.Key()))

			err = rebPlasmaWriter.InsertKV(itr.Key(), val)
			if err != nil {
				logging.Errorf("Consumer::CreateTempPlasmaStore [%s:%d] vb: %v key: %s failed to insert, err: %v",
					c.workerName, c.Pid(), vb, string(itr.Key()), err)
				continue
			}
		}
	}
	return nil
}

// PurgePlasmaRecords cleans up temporary plasma store created during rebalance file transfer
// Cleans up KV records from original plasma store on source after they are transferred
// to another eventing node
func (c *Consumer) PurgePlasmaRecords(vb uint16) error {
	vbPlasmaDir := fmt.Sprintf("%v/reb_%v_%v_timer.data", c.eventingDir, vb, c.app.AppName)
	err := os.RemoveAll(vbPlasmaDir)
	if err != nil {
		logging.Errorf("Consumer::PurgePlasmaRecords [%s:%d] vb: %v dir: %v Failed to remove plasma dir post vb ownership takeover by another node, err: %v",
			c.workerName, c.Pid(), vb, vbPlasmaDir, err)
		return err
	}

	r := c.vbPlasmaStore.NewReader()
	w := c.vbPlasmaStore.NewWriter()
	snapshot := c.vbPlasmaStore.NewSnapshot()
	defer snapshot.Close()

	itr, err := r.NewSnapshotIterator(snapshot)
	if err != nil {
		logging.Errorf("Consumer::PurgePlasmaRecords [%s:%d] vb: %v Failed to create snapshot, err: %v",
			c.workerName, c.Pid(), vb, err)
		return err
	}

	for itr.SeekFirst(); itr.Valid(); itr.Next() {
		keyPrefix := []byte(fmt.Sprintf("vb_%v::", vb))

		if bytes.Compare(itr.Key(), keyPrefix) > 0 {
			_, err := w.LookupKV(itr.Key())
			if err != nil && err != plasma.ErrItemNoValue {
				logging.Errorf("Consumer::PurgePlasmaRecords [%s:%d] vb: %v key: %s failed lookup, err: %v",
					c.workerName, c.Pid(), vb, string(itr.Key()), err)
				continue
			}

			err = w.DeleteKV(itr.Key())
			if err != nil {
				logging.Tracef("Consumer::PurgePlasmaRecords [%s:%d] vb: %v deleted key: %s  from source plasma",
					c.workerName, c.Pid(), vb, string(itr.Key()))
			}
		}
	}

	return nil
}

func (c *Consumer) copyPlasmaRecords(vb uint16, dTimerDir string) error {
	pStore, err := c.openPlasmaStore(dTimerDir)
	if err != nil {
		logging.Errorf("Consumer::copyPlasmaRecords [%s:%d] vb: %v Failed to create plasma instance for plasma data dir: %v received, err: %v",
			c.workerName, c.Pid(), vb, dTimerDir, err)
		return err
	}
	plasmaStoreWriter := c.vbPlasmaStore.NewWriter()

	r := pStore.NewReader()
	w := pStore.NewWriter()
	snapshot := pStore.NewSnapshot()

	defer os.RemoveAll(dTimerDir)
	defer pStore.Close()
	defer snapshot.Close()

	itr, err := r.NewSnapshotIterator(snapshot)
	if err != nil {
		logging.Errorf("Consumer::copyPlasmaRecords [%s:%d] vb: %v Failed to create snapshot, err: %v",
			c.workerName, c.Pid(), vb, err)
		return err
	}

	for itr.SeekFirst(); itr.Valid(); itr.Next() {

		val, err := w.LookupKV(itr.Key())
		if err != nil && err != plasma.ErrItemNoValue {
			logging.Errorf("Consumer::copyPlasmaRecords [%s:%d] key: %v Failed to lookup, err: %v",
				c.workerName, c.Pid(), string(itr.Key()), err)
			continue
		} else {
			logging.Tracef("Consumer::copyPlasmaRecords [%s:%d] Inserting key: %v Lookup value: %v",
				c.workerName, c.Pid(), string(itr.Key()), string(val))
		}

		err = plasmaStoreWriter.InsertKV(itr.Key(), val)
		if err != nil {
			logging.Errorf("Consumer::copyPlasmaRecords [%s:%d] key: %v Failed to insert, err: %v",
				c.workerName, c.Pid(), itr.Key(), err)
			continue
		}
	}

	return nil
}
