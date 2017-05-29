package consumer

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/couchbase/eventing/util"
	"github.com/couchbase/gocb"
	"github.com/couchbase/indexing/secondary/logging"
	"github.com/couchbase/nitro/plasma"
)

var plasmaInsertKV = func(args ...interface{}) error {
	c := args[0].(*Consumer)
	w := args[1].(*plasma.Writer)
	k := args[2].(string)
	v := args[3].(string)
	vb := args[4].(uint16)

	token := w.BeginTx()
	_, err := w.LookupKV([]byte(k))

	// Purging if a previous entry for key already exists. This behaviour of plasma
	// might change in future - presently plasma allows duplicate values for same key
	if err == nil || err == plasma.ErrItemNoValue {
		w.DeleteKV([]byte(k))
	}

	err = w.InsertKV([]byte(k), []byte(v))
	if err != nil {
		logging.Errorf("CRPO[%s:%s:%s:%d] Key: %v vb: %v Failed to insert into plasma store, err: %v",
			c.app.AppName, c.workerName, c.tcpPort, c.Pid(), k, vb, err)
	} else {
		logging.Debugf("CRPO[%s:%s:%s:%d] Key: %v value: %v vb: %v Successfully inserted into plasma store, err: %v",
			c.app.AppName, c.workerName, c.tcpPort, c.Pid(), k, v, vb, err)
	}
	w.EndTx(token)

	return err
}

func (c *Consumer) plasmaPersistAll() {
	for {
		select {
		case <-c.persistAllTicker.C:
			for vb, s := range c.vbPlasmaStoreMap {
				if s == nil {
					continue
				}

				s.PersistAll()
				seqNo := c.vbProcessingStats.getVbStat(vb, "plasma_last_seq_no_stored")
				c.vbProcessingStats.updateVbStat(vb, "plasma_last_seq_no_persisted", seqNo)
			}

		case <-c.stopPlasmaPersistCh:
			return
		}
	}
}

func (c *Consumer) vbTimerProcessingWorkerAssign(initWorkers bool) {
	vbsOwned := c.getVbsOwned()

	vbsPerWorker := len(vbsOwned) / c.timerProcessingWorkerCount

	var vb int
	startVb := vbsOwned[0]

	vbsCountPerWorker := make([]int, c.timerProcessingWorkerCount)
	for i := 0; i < c.timerProcessingWorkerCount; i++ {
		vbsCountPerWorker[i] = vbsPerWorker
		vb += vbsPerWorker
	}

	remainingVbs := len(vbsOwned) - vb
	for i := 0; i < remainingVbs; i++ {
		vbsCountPerWorker[i] = vbsCountPerWorker[i] + 1
	}

	if initWorkers {
		for i, v := range vbsCountPerWorker {

			worker := &timerProcessingWorker{
				c:  c,
				id: i,
				signalProcessTimerPlasmaCloseCh: make(chan uint16),
				stopCh:                make(chan bool, 1),
				timerProcessingTicker: time.NewTicker(c.timerProcessingTickInterval),
			}

			vbsAssigned := make([]uint16, v)

			for j := 0; j < v; j++ {
				vbsAssigned[j] = startVb
				c.timerProcessingVbsWorkerMap[startVb] = worker
				c.vbProcessingStats.updateVbStat(startVb, "timer_processing_worker", fmt.Sprintf("timer_%d", i))
				startVb++
			}

			worker.vbsAssigned = vbsAssigned

			c.timerProcessingRunningWorkers = append(c.timerProcessingRunningWorkers, worker)
			c.timerProcessingWorkerSignalCh[worker] = make(chan bool, 1)

			logging.Debugf("CRPO[%s:%s:timer_%d:%s:%d] Initial Timer routine vbs assigned len: %d dump: %v",
				c.app.AppName, c.workerName, worker.id, c.tcpPort, c.Pid(), len(vbsAssigned), vbsAssigned)
		}
	} else {

		for i, v := range vbsCountPerWorker {

			vbsAssigned := make([]uint16, v)

			c.timerRWMutex.Lock()
			for j := 0; j < v; j++ {
				vbsAssigned[j] = startVb
				c.timerProcessingVbsWorkerMap[startVb] = c.timerProcessingRunningWorkers[i]
				c.vbProcessingStats.updateVbStat(startVb, "timer_processing_worker", fmt.Sprintf("timer_%d", i))
				startVb++
			}

			c.timerProcessingRunningWorkers[i].vbsAssigned = vbsAssigned
			c.timerRWMutex.Unlock()

			logging.Debugf("CRPO[%s:%s:timer_%d:%s:%d] Timer routine vbs assigned len: %d dump: %v",
				c.app.AppName, c.workerName, i, c.tcpPort, c.Pid(), len(vbsAssigned), vbsAssigned)
		}
	}
}

func (r *timerProcessingWorker) getVbsOwned() []uint16 {
	return r.vbsAssigned
}

func (r *timerProcessingWorker) processTimerEvents() {
	vbsOwned := r.getVbsOwned()

	currTimer := time.Now().UTC().Format(time.RFC3339)
	nextTimer := time.Now().UTC().Add(time.Second).Format(time.RFC3339)

	for _, vb := range vbsOwned {
		vbKey := fmt.Sprintf("%s_vb_%s", r.c.app.AppName, strconv.Itoa(int(vb)))

		var vbBlob vbucketKVBlob
		var cas uint64

		util.Retry(util.NewFixedBackoff(bucketOpRetryInterval), getOpCallback, r.c, vbKey, &vbBlob, &cas, false)

		if vbBlob.CurrentProcessedTimer == "" {
			r.c.vbProcessingStats.updateVbStat(vb, "currently_processed_timer", currTimer)
		} else {
			r.c.vbProcessingStats.updateVbStat(vb, "currently_processed_timer", vbBlob.CurrentProcessedTimer)
		}

		if vbBlob.NextTimerToProcess == "" {
			r.c.vbProcessingStats.updateVbStat(vb, "next_timer_to_process", nextTimer)
		} else {
			r.c.vbProcessingStats.updateVbStat(vb, "next_timer_to_process", vbBlob.NextTimerToProcess)
		}

		r.c.vbProcessingStats.updateVbStat(vb, "last_processed_timer_event", vbBlob.LastProcessedTimerEvent)
		r.c.vbProcessingStats.updateVbStat(vb, "plasma_last_seq_no_persisted", vbBlob.PlasmaPersistedSeqNo)
	}

	for {
		select {
		case <-r.stopCh:
			return
		case <-r.timerProcessingTicker.C:
		case vb := <-r.signalProcessTimerPlasmaCloseCh:
			// Rebalance takeover routine will send signal on this channel to signify
			// stopping of any plasma.Writer instance for a specific vbucket
			r.c.timerRWMutex.Lock()
			_, ok := r.c.vbPlasmaReader[vb]
			if ok {
				delete(r.c.vbPlasmaReader, vb)
			}

			delete(r.c.timerProcessingVbsWorkerMap, vb)
			r.c.timerRWMutex.Unlock()

			// sends ack message back to rebalance takeover routine, so that it could
			// safely call Close() on vb specific plasma store
			r.c.signalProcessTimerPlasmaCloseAckCh <- vb
		}

		vbsOwned = r.getVbsOwned()
		for _, vb := range vbsOwned {
			currTimer := r.c.vbProcessingStats.getVbStat(vb, "currently_processed_timer").(string)

			r.c.timerRWMutex.RLock()
			_, ok := r.c.vbPlasmaReader[vb]
			r.c.timerRWMutex.RUnlock()
			if !ok {
				continue
			}

			// Make sure time processing isn't going ahead of system clock
			ts, err := time.Parse(tsLayout, currTimer)
			if err != nil {
				logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d Failed to parse currtime: %v err: %v",
					r.c.app.AppName, r.c.workerName, r.c.tcpPort, r.c.Pid(), vb, currTimer, err)
				continue
			}

			if ts.After(time.Now()) {
				continue
			}

		retryLookup:
			// For memory management
			r.c.timerRWMutex.RLock()
			token := r.c.vbPlasmaReader[vb].BeginTx()
			v, err := r.c.vbPlasmaReader[vb].LookupKV([]byte(currTimer))
			r.c.vbPlasmaReader[vb].EndTx(token)
			r.c.timerRWMutex.RUnlock()

			if err != nil && err != plasma.ErrItemNotFound {
				logging.Errorf("CRPO[%s:%s:timer_%d:%s:%d] vb: %d Failed to lookup currTimer: %v err: %v",
					r.c.app.AppName, r.c.workerName, r.id, r.c.tcpPort, r.c.Pid(), vb, currTimer, err)
				goto retryLookup
			}

			r.c.updateTimerStats(vb)

			lastTimerEvent := r.c.vbProcessingStats.getVbStat(vb, "last_processed_timer_event")
			if lastTimerEvent != "" {
				startProcess := false

				// Previous timer entry wasn't processed completely, hence will resume from where things were left
				timerEvents := strings.Split(string(v), ",{")
				if len(timerEvents) == 0 || len(timerEvents[0]) == 0 {
					continue
				}

				var timer byTimerEntry
				err = json.Unmarshal([]byte(timerEvents[0]), &timer)
				if err != nil {
					logging.Errorf("CRPO[%s:%s:timer_%d:%s:%d] vb: %d Failed to unmarshal timerEvent: %v err: %v",
						r.c.app.AppName, r.c.workerName, r.id, r.c.tcpPort, r.c.Pid(), vb, timerEvents[0], err)
				} else {
					if lastTimerEvent == timer.DocID {
						startProcess = true
					}
				}

				if len(timerEvents) > 1 {
					for _, event := range timerEvents[1:] {

						if len(event) == 0 {
							continue
						}

						event := "{" + event
						err = json.Unmarshal([]byte(event), &timer)
						if err != nil {
							logging.Errorf("CRPO[%s:%s:timer_%d:%s:%d] vb: %d Failed to unmarshal timerEvent: %v err: %v",
								r.c.app.AppName, r.c.workerName, r.id, r.c.tcpPort, r.c.Pid(), vb, event, err)
						}

						if startProcess {
							r.c.timerEntryCh <- &timer
							r.c.vbProcessingStats.updateVbStat(vb, "last_processed_timer_event", timer.DocID)
						} else if lastTimerEvent == timer.DocID {
							startProcess = true
						}
					}
				}

				r.c.timerRWMutex.RLock()
				token = r.c.vbPlasmaReader[vb].BeginTx()
				err = r.c.vbPlasmaReader[vb].DeleteKV([]byte(currTimer))
				r.c.vbPlasmaReader[vb].EndTx(token)
				r.c.timerRWMutex.RUnlock()

				if err != nil {
					logging.Errorf("CRPO[%s:%s:timer_%d:%s:%d] vb: %d key: %v Failed to delete from plasma handle, err: %v",
						r.c.app.AppName, r.c.workerName, r.id, r.c.tcpPort, r.c.Pid(), vb, currTimer, err)
				}

				continue
			}

			if len(v) == 0 {
				continue
			}

			timerEvents := strings.Split(string(v), ",{")

			r.c.processTimerEvent(currTimer, timerEvents[0], vb, false)

			if len(timerEvents) > 1 {
				for _, event := range timerEvents[1:] {
					event := "{" + event
					r.c.processTimerEvent(currTimer, event, vb, true)
				}
			}

			r.c.vbProcessingStats.updateVbStat(vb, "last_processed_timer_event", "")

			r.c.timerRWMutex.RLock()
			token = r.c.vbPlasmaReader[vb].BeginTx()
			err = r.c.vbPlasmaReader[vb].DeleteKV([]byte(currTimer))
			r.c.vbPlasmaReader[vb].EndTx(token)
			r.c.timerRWMutex.RUnlock()

			if err != nil {
				logging.Errorf("CRPO[%s:%s:timer_%d:%s:%d] vb: %d key: %v Failed to delete from byTimer plasma handle, err: %v",
					r.c.app.AppName, r.c.workerName, r.id, r.c.tcpPort, r.c.Pid(), vb, currTimer, err)
			}

			r.c.updateTimerStats(vb)
		}
	}
}

func (c *Consumer) processTimerEvent(currTimer, event string, vb uint16, updateStats bool) {
	var timer byTimerEntry
	err := json.Unmarshal([]byte(event), &timer)
	if err != nil {
		logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d processTimerEvent Failed to unmarshal timerEvent: %v err: %v",
			c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, event, err)
	} else {
		c.timerEntryCh <- &timer

		key := fmt.Sprintf("%v::%v::%v", currTimer, timer.CallbackFn, timer.DocID)
		c.timerRWMutex.RLock()
		err = c.vbPlasmaReader[vb].DeleteKV([]byte(key))
		c.timerRWMutex.RUnlock()
		if err != nil {
			logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d key: %v Failed to delete from plasma handle, err: %v",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, key, err)
		}
	}

	if updateStats {
		c.vbProcessingStats.updateVbStat(vb, "last_processed_timer_event", timer.DocID)
	}
}

func (c *Consumer) updateTimerStats(vb uint16) {

	tsLayout := "2006-01-02T15:04:05Z"

	nTimerTs := c.vbProcessingStats.getVbStat(vb, "next_timer_to_process").(string)
	c.vbProcessingStats.updateVbStat(vb, "currently_processed_timer", nTimerTs)

	nextTimer, err := time.Parse(tsLayout, nTimerTs)
	if err != nil {
		logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d Failed to parse time: %v err: %v",
			c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, nTimerTs, err)
	}

	c.vbProcessingStats.updateVbStat(vb, "next_timer_to_process",
		nextTimer.UTC().Add(time.Second).Format(time.RFC3339))

}

func (c *Consumer) storeTimerEvent(vb uint16, seqNo uint64, expiry uint32, key string, xMeta *xattrMetadata) error {

	// Steps:
	// Lookup in byId plasma handle
	// If ENOENT, then insert KV pair in byId plasma handle
	// then insert in byTimer plasma handle as well

	plasmaWriterHandle, ok := c.vbPlasmaWriter[vb]
	if !ok {
		logging.Errorf("CRPO[%s:%s:%s:%d] Key: %v, failed to find plasma handle associated to vb: %v",
			c.app.AppName, c.workerName, c.tcpPort, c.Pid(), key, vb)
		return errPlasmaHandleMissing
	}

	entriesToPrune := 0
	timersToKeep := make([]string, 0)

	for _, timer := range xMeta.Timers {
		// check if timer timestamp has already passed, if yes then skip adding it to plasma
		t := strings.Split(timer, "::")[0]

		ts, err := time.Parse(tsLayout, t)

		if err != nil {
			logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d Failed to parse time: %v err: %v",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, timer, err)
			continue
		}

		if !ts.After(time.Now()) {
			logging.Debugf("CRPO[%s:%s:%s:%d] vb: %d Not adding timer event: %v to plasma because it was timer in past",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, ts)
			entriesToPrune++
			continue
		}

		timersToKeep = append(timersToKeep, timer)

		timerKey := fmt.Sprintf("%v::%v", timer, key)

		// Creating transaction for memory management
		token := plasmaWriterHandle.BeginTx()
		_, err = plasmaWriterHandle.LookupKV([]byte(timerKey))
		plasmaWriterHandle.EndTx(token)

		if err == plasma.ErrItemNotFound {

			util.Retry(util.NewFixedBackoff(plasmaOpRetryInterval), plasmaInsertKV, c, plasmaWriterHandle, timerKey, "", vb)

			timerData := strings.Split(timer, "::")
			ts, cbFunc := timerData[0], timerData[1]

		retryPlasmaLookUp:

			token = plasmaWriterHandle.BeginTx()
			tv, tErr := plasmaWriterHandle.LookupKV([]byte(ts))
			plasmaWriterHandle.EndTx(token)

			if tErr == plasma.ErrItemNotFound {
				v := byTimerEntry{
					DocID:      key,
					CallbackFn: cbFunc,
				}

				encodedVal, mErr := json.Marshal(&v)
				if mErr != nil {
					logging.Errorf("CRPO[%s:%s:%s:%d] Key: %v JSON marshal failed, err: %v",
						c.app.AppName, c.workerName, c.tcpPort, c.Pid(), timerKey, err)
					continue
				}

				util.Retry(util.NewFixedBackoff(plasmaOpRetryInterval), plasmaInsertKV, c,
					plasmaWriterHandle, ts, string(encodedVal), vb)

			} else if tErr != nil {

				logging.Errorf("CRPO[%s:%s:%s:%d] vb: %d Failed to lookup entry for ts: %v err: %v. Retrying..",
					c.app.AppName, c.workerName, c.tcpPort, c.Pid(), vb, ts, err)
				goto retryPlasmaLookUp

			} else {
				v := byTimerEntry{
					DocID:      key,
					CallbackFn: cbFunc,
				}

				encodedVal, mErr := json.Marshal(&v)
				if mErr != nil {
					logging.Errorf("CRPO[%s:%s:%s:%d] Key: %v JSON marshal failed, err: %v",
						c.app.AppName, c.workerName, c.tcpPort, c.Pid(), timerKey, err)
					continue
				}

				timerVal := fmt.Sprintf("%v,%v", string(tv), string(encodedVal))

				util.Retry(util.NewFixedBackoff(plasmaOpRetryInterval), plasmaInsertKV, c,
					plasmaWriterHandle, ts, timerVal, vb)
			}
		} else if err != nil && err != plasma.ErrItemNoValue {
			logging.Errorf("CRPO[%s:%s:%s:%d] Key: %v plasmaWriterHandle returned, err: %v",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), timerKey, err)
		}
	}

	if entriesToPrune > 0 {
		// Cleaning up timer event entry record which point to time in past
		docF := c.gocbBucket.MutateIn(key, 0, expiry)
		docF.UpsertEx(xattrTimerPath, timersToKeep, gocb.SubdocFlagXattr|gocb.SubdocFlagCreatePath)
		docF.UpsertEx(xattrCasPath, "${Mutation.CAS}", gocb.SubdocFlagXattr|gocb.SubdocFlagCreatePath|gocb.SubdocFlagUseMacros)

		_, err := docF.Execute()
		if err != nil {
			logging.Errorf("CRPO[%s:%s:%s:%d]  Key: %v vb: %v, Failed to prune timer records from past, err: %v",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), key, vb, err)
		} else {
			logging.Debugf("CRPO[%s:%s:%s:%d]  Key: %v vb: %v, timer records in xattr: %v",
				c.app.AppName, c.workerName, c.tcpPort, c.Pid(), key, vb, timersToKeep)
		}
	}

	c.vbProcessingStats.updateVbStat(vb, "plasma_last_seq_no_stored", seqNo)
	return nil
}
