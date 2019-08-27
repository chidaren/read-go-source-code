### sync.mutex 源码解析

```go

// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sync provides basic synchronization primitives such as mutual
// exclusion locks. Other than the Once and WaitGroup types, most are intended
// for use by low-level library routines. Higher-level synchronization is
// better done via channels and communication.
//
// Values containing the types defined in this package should not be copied.
package sync

import (
	"internal/race"
	"sync/atomic"
	"unsafe"
)

// panic
func throw(string) // provided by runtime

// A Mutex is a mutual exclusion lock.
// The zero value for a Mutex is an unlocked mutex.
//
// A Mutex must not be copied after first use.

// 锁功能全部由state 和sema 变量的值来提供, state 用它前4个bit的值来表示不同状态, 初始是 0000, 加锁后是 0001
type Mutex struct {
	state int32
	sema  uint32
}

// A Locker represents an object that can be locked and unlocked.
type Locker interface {
	Lock()
	Unlock()
}

const (
	mutexLocked = 1 << iota // mutex is locked
	mutexWoken  // 2
	mutexStarving   // 4
	mutexWaiterShift = iota // 3

/* 
一共有两种状态, 普通模式和饥饿模式,  普通模式下, 所有的G都会进入一个队列, 新来的会排在队列头, 因为新来的这个时候刚好再cpu 时间片内, 如果有G超过1ms没有拿到锁,
则会进入饥饿模式, 饥饿模式下, 所有新来的G不会不会尝试去获取锁, 也不会尝试自旋, 即使当前mutex 已经被unlock, 它只会排到队列尾部. 如果一个G拿到锁了, 并且发现它处于队列
尾部, 或者他获取到锁的时间不超过1ms, 则又会转入普通模式.

如果一个G正在进行自旋, 会设置 MutexWoken 为true, 那么如果有G释放了锁, 不会去唤醒其他的G, 因为如果唤醒其他G, 在多逻辑P的情况下, 可能会产生抢占, 所以最好是啥都不做, 让处于
自旋的G直接拿到锁去继续运行, 因为不需要等待调度, 并且这个G已经获得时间片在进行调度了, 这个算是一个小优化

普通模式性能好, 一个G可以多次获取锁, 即使有其他很多等待的G(因为普通模式下新来的G会进入队列头部). 饥饿模式对于极端情况有比较好的效果

*/
	// Mutex fairness.
	//
	// Mutex can be in 2 modes of operations: normal and starvation.
	// In normal mode waiters are queued in FIFO order, but a woken up waiter
	// does not own the mutex and competes with new arriving goroutines over
	// the ownership. New arriving goroutines have an advantage -- they are
	// already running on CPU and there can be lots of them, so a woken up
	// waiter has good chances of losing. In such case it is queued at front
	// of the wait queue. If a waiter fails to acquire the mutex for more than 1ms,
	// it switches mutex to the starvation mode.
	//
	// In starvation mode ownership of the mutex is directly handed off from
	// the unlocking goroutine to the waiter at the front of the queue.
	// New arriving goroutines don't try to acquire the mutex even if it appears
	// to be unlocked, and don't try to spin. Instead they queue themselves at
	// the tail of the wait queue.
	//
	// If a waiter receives ownership of the mutex and sees that either
	// (1) it is the last waiter in the queue, or (2) it waited for less than 1 ms,
	// it switches mutex back to normal operation mode.
	//
	// Normal mode has considerably better performance as a goroutine can acquire
	// a mutex several times in a row even if there are blocked waiters.
	// Starvation mode is important to prevent pathological cases of tail latency.
	starvationThresholdNs = 1e6
)

// Lock locks m.
// If the lock is already in use, the calling goroutine
// blocks until the mutex is available.
func (m *Mutex) Lock() {
    // Fast path: grab unlocked mutex.
    // m.state 第一个bit 为0 时候, 表示非占用
	if atomic.CompareAndSwapInt32(&m.state, 0, mutexLocked) {
		if race.Enabled {
			race.Acquire(unsafe.Pointer(m))
		}
		return
	}

	var waitStartTime int64
	// 是否进入饥饿状态
	starving := false
	// 当前G是否
	awoke := false
	// 自旋计数
	iter := 0
	old := m.state
	for {
		// Don't spin in starvation mode, ownership is handed off to waiters
        // so we won't be able to acquire the mutex anyway.
        
        // 如果不是饥饿模式, 并且可以执行自旋操作(比如逻辑单核情况下就不能执行自旋, 因为徒劳无功), 如果自旋次数过多, 则说明临时获取不到锁, 可能需要让出时间片, 这里就是切换
        // 当前G的状态
		if old&(mutexLocked|mutexStarving) == mutexLocked && runtime_canSpin(iter) {
			// Active spinning makes sense.
			// Try to set mutexWoken flag to inform Unlock
            // to not wake other blocked goroutines.
            
			// 如果当前没有在进行自旋操作, 并且当前有在等待的队列, 也就是说当前值得去自旋等待锁, 那么就设置mutexWoken 标志为true, 代表现在有G处于活跃状态
			// 所以如果有G unlock了这个锁, 不要去唤醒其他G, 因为唤醒后只是将其他G 放到可运行队列, 还得等cpu, 这里可以直接让当前的G 获取锁, 算是一个优化
			if !awoke && old&mutexWoken == 0 && old>>mutexWaiterShift != 0 &&
				atomic.CompareAndSwapInt32(&m.state, old, old|mutexWoken) {
				awoke = true
            }
            // 执行自旋操作, 尝试30次, 底层会执行 pause指令, cpu 不会做不必要的优化
			runtime_doSpin()
            iter++
            // 更新old 再次检查是当前mutex 是不是锁定状态
			old = m.state
			continue
        }
        
        // --------------- 如果自旋了一定次数后, 还是没有获取到锁, 或者目前mutex 为未锁定状态, 或者是已经进入饥饿模式----------------

		new := old
        // Don't try to acquire starving mutex, new arriving goroutines must queue.
        // 如果 old 是普通模式, 则将会直接上锁, 但是如果不是普通模式, 已经进入了饥饿模式, 则不尝试去获取锁
		if old&mutexStarving == 0 {
			new |= mutexLocked
        }
        // 如果old 是饥饿模式,或者old 还是锁定状态, 则对 new 的mutexWaiterShift 后的位+1, 也就是添加到队列尾, 进入Gwaitting 状态
		if old&(mutexLocked|mutexStarving) != 0 {
			new += 1 << mutexWaiterShift
		}
		// The current goroutine switches mutex to starvation mode.
		// But if the mutex is currently unlocked, don't do the switch.
		// Unlock expects that starving mutex has waiters, which will not
		// be true in this case.
		// 如果当前mutex 为锁定状态, 并且已经超过1ms 还是没有获取到锁, 则要进入饥饿状态
		if starving && old&mutexLocked != 0 {
			new |= mutexStarving
		}
		if awoke {
			// The goroutine has been woken from sleep,
			// so we need to reset the flag in either case.
			if new&mutexWoken == 0 {
				throw("sync: inconsistent mutex state")
			}
			new &^= mutexWoken
		}

		// 尝试更新 mutex 状态, 如果有其他程序这时候修改了mutex 状态, 则重来
		if atomic.CompareAndSwapInt32(&m.state, old, new) {

			// 如果old 不是锁定状态, 并且不是饥饿模式, 
			if old&(mutexLocked|mutexStarving) == 0 {
				break // locked the mutex with CAS
			}
			// If we were already waiting before, queue at the front of the queue.
			queueLifo := waitStartTime != 0
			if waitStartTime == 0 {
				// 使用单调时间(当前系统启动后流逝的时间), 防止用户修改系统时间引起程序异常
				waitStartTime = runtime_nanotime()
            }
            
            // 进入休眠, 等待信号量唤醒, 如果之前已经等待过, 这次将此G加到队列头部, 也就是让他有更高优先级被先唤醒
			runtime_SemacquireMutex(&m.sema, queueLifo)

			// 被唤醒后, 如果等待超过 starvationThresholdNs 也就是1ms 则需要进入饥饿模式
			starving = starving || runtime_nanotime()-waitStartTime > starvationThresholdNs
			old = m.state
			if old&mutexStarving != 0 {
				// If this goroutine was woken and mutex is in starvation mode,
				// ownership was handed off to us but mutex is in somewhat
				// inconsistent state: mutexLocked is not set and we are still
				// accounted as waiter. Fix that.
				if old&(mutexLocked|mutexWoken) != 0 || old>>mutexWaiterShift == 0 {
					throw("sync: inconsistent mutex state")
				}
				delta := int32(mutexLocked - 1<<mutexWaiterShift)
				if !starving || old>>mutexWaiterShift == 1 {
					// Exit starvation mode.
					// Critical to do it here and consider wait time.
					// Starvation mode is so inefficient, that two goroutines
					// can go lock-step infinitely once they switch mutex
					// to starvation mode.
					delta -= mutexStarving
				}
				atomic.AddInt32(&m.state, delta)
				break
			}
			awoke = true
			iter = 0
		} else {
			old = m.state
		}
	}

	if race.Enabled {
		race.Acquire(unsafe.Pointer(m))
	}
}

// Unlock unlocks m.
// It is a run-time error if m is not locked on entry to Unlock.
//
// A locked Mutex is not associated with a particular goroutine.
// It is allowed for one goroutine to lock a Mutex and then
// arrange for another goroutine to unlock it.
func (m *Mutex) Unlock() {
	if race.Enabled {
		_ = m.state
		race.Release(unsafe.Pointer(m))
	}

    // Fast path: drop lock bit.
    // 尝试去解锁, 只有mutexLocked 是true 才成功
	new := atomic.AddInt32(&m.state, -mutexLocked)
	// 如果当前不是锁定状态也就是 m.state 的mutexLocked 不是1, 那么减一后再加一就是0, 则这里需要panic
	if (new+mutexLocked)&mutexLocked == 0 {
		throw("sync: unlock of unlocked mutex")
    }
    
    // 如果当前处于普通模式, 也就是非饥饿模式
	if new&mutexStarving == 0 {
		old := new
		for {
			// If there are no waiters or a goroutine has already
			// been woken or grabbed the lock, no need to wake anyone.
			// In starvation mode ownership is directly handed off from unlocking
			// goroutine to the next waiter. We are not part of this chain,
			// since we did not observe mutexStarving when we unlocked the mutex above.
            // So get off the way.
			// mutexWaiterShift bit 为等待的G, 如果mutexWaiterShift 后全为0, 则表示没有等待的G, 这里不做任何处理, 直接返回
			// mutexWoken 如果是true, 则这时候不会去主动唤醒, 因为已经有一个G在进行自旋了
			if old>>mutexWaiterShift == 0 || old&(mutexLocked|mutexWoken|mutexStarving) != 0 {
				return
			}
			// Grab the right to wake someone.
			// 等待队列减一, 然后设置mutex 状态为 mutexWoken, 也就是代表马上要唤醒一个G了
            new = (old - 1<<mutexWaiterShift) | mutexWoken

			// 如果没有没有在等待的, 或者说当前mutex 又被锁定了, 也直接返回, 只有在没有G进行自旋, 并且还有很多G等待锁的状态下, 这里才会去主动唤醒
            // 尝试去唤醒一G, 并设置新的mutex 状态
			if atomic.CompareAndSwapInt32(&m.state, old, new) {
				runtime_Semrelease(&m.sema, false)
				return
			}
			old = m.state
		}
	} else {
		// Starving mode: handoff mutex ownership to the next waiter.
		// Note: mutexLocked is not set, the waiter will set it after wakeup.
		// But mutex is still considered locked if mutexStarving is set,
        // so new coming goroutines won't acquire it.
		// 饥饿模式下, 什么都不用管, 直接主动去通知唤醒G, 
		// 这里发送释放信号, 唤起等待的G, 为true 则唤起第一个等待的G, 也就是说唤起队列尾的G
		runtime_Semrelease(&m.sema, true)
	}
}



```