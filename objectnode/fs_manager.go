// Copyright 2018 The ChubaoFS Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package objectnode

import (
	"errors"
	"sync"

	"github.com/chubaofs/chubaofs/proto"

	"github.com/chubaofs/chubaofs/sdk/master"
	"github.com/chubaofs/chubaofs/util/log"
)

type VolumeManager struct {
	masters    []string
	mc         *master.MasterClient
	volumes    map[string]*Volume // mapping: volume name -> *Volume
	volMu      sync.RWMutex
	volInitMap sync.Map // mapping: volume name -> *sync.Mutex
	store      Store
	closeOnce  sync.Once
}

func (m *VolumeManager) Release(volName string) {
	m.volMu.Lock()
	vol, has := m.volumes[volName]
	if has {
		delete(m.volumes, volName)
		log.LogDebugf("Release: release volume: volume(%v)", volName)
	}
	m.volMu.Unlock()
	if has && vol != nil {
		if closeErr := vol.Close(); closeErr != nil {
			log.LogErrorf("Release: close volume fail: volume(%v) err(%v)", volName, closeErr)
		}
	}
}

func (m *VolumeManager) Volume(volName string) (*Volume, error) {
	return m.loadVolume(volName)
}

func (m *VolumeManager) syncVolumeInit(volume string) (releaseFunc func()) {
	value, _ := m.volInitMap.LoadOrStore(volume, new(sync.Mutex))
	var initMu = value.(*sync.Mutex)
	initMu.Lock()
	log.LogDebugf("syncVolumeInit: get volume init lock: volume(%v)", volume)
	return func() {
		initMu.Unlock()
		m.volInitMap.Delete(volume)
		log.LogDebugf("syncVolumeInit: release volume init lock: volume(%v)", volume)
	}
}

func (m *VolumeManager) loadVolume(volName string) (*Volume, error) {
	var err error
	var volume *Volume
	var exist bool
	m.volMu.RLock()
	volume, exist = m.volumes[volName]
	m.volMu.RUnlock()
	if !exist {
		var release = m.syncVolumeInit(volName)
		m.volMu.RLock()
		volume, exist = m.volumes[volName]
		if exist {
			m.volMu.RUnlock()
			release()
			return volume, nil
		}
		m.volMu.RUnlock()

		var onAsyncTaskError AsyncTaskErrorFunc = func(err error) {
			switch err {
			case proto.ErrVolNotExists:
				m.Release(volName)
			default:
			}
		}
		var config = &VolumeConfig{
			Volume:           volName,
			Masters:          m.masters,
			OnAsyncTaskError: onAsyncTaskError,
		}
		if volume, err = NewVolume(config); err != nil {
			release()
			return nil, err
		}
		ak, sk := volume.OSSSecure()
		log.LogDebugf("[loadVolume] load Volume: Name[%v] AccessKey[%v] SecretKey[%v]", volName, ak, sk)

		m.volMu.Lock()
		m.volumes[volName] = volume
		volume.vm = m
		m.volMu.Unlock()
		release()

		volume.loadOSSMeta()
	}

	return volume, nil
}

// Release all
func (m *VolumeManager) Close() {
	m.volMu.Lock()
	defer m.volMu.Unlock()
	for volKey, vol := range m.volumes {
		_ = vol.Close()
		log.LogDebugf("release Volume %v", volKey)
	}
	m.volumes = make(map[string]*Volume)
}

func (m *VolumeManager) InitStore(s Store) {
	s.Init(m)
	m.store = s
}

func (m *VolumeManager) GetStore() (Store, error) {
	if m.store == nil {
		return nil, errors.New("store not init")
	}
	return m.store, nil
}

func (m *VolumeManager) InitMasterClient(masters []string, useSSL bool) {
	m.mc = master.NewMasterClient(masters, useSSL)
}

func (m *VolumeManager) GetMasterClient() (*master.MasterClient, error) {
	if m.mc == nil {
		return nil, errors.New("master client not init")
	}
	return m.mc, nil
}

func NewVolumeManager(masters []string) *VolumeManager {
	vc := &VolumeManager{
		volumes: make(map[string]*Volume),
		masters: masters,
	}
	return vc
}
