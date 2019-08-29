### sync.Atomic 源码分析

``` go


// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package atomic

import (
	"unsafe"
)

// A Value provides an atomic load and store of a consistently typed value.
// The zero value for a Value returns nil from Load.
// Once Store has been called, a Value must not be copied.
//
// A Value must not be copied after first use.
type Value struct {
	v interface{}
}

// ifaceWords is interface{} internal representation.
// go 内部变量等数据存储的结构都是一个类型, 一个数据
type ifaceWords struct {
	typ  unsafe.Pointer
	data unsafe.Pointer
}

// Load returns the value set by the most recent Store.
// It returns nil if there has been no call to Store for this Value.

// 这里已经保证了不会v 的typ 是一直不变的, 因为如果有变化, 写入的时候会panic
func (v *Value) Load() (x interface{}) {
	vp := (*ifaceWords)(unsafe.Pointer(v))
	typ := LoadPointer(&vp.typ)

	// 先原子的load 出vp, 判断类型, 检测是否已经有最少一次完整的写入
	if typ == nil || uintptr(typ) == ^uintptr(0) {
		// First store not yet completed.
		return nil
	}

	// 原子load 出数据
	data := LoadPointer(&vp.data)
	// load 调用的时候, 先原子load 出类型, 类型肯定不会变, 所以在原子load 出数据, 然后复制给程序的返回值, 多次调用会创建多个x, x指针指向的就是目前真是数据变量地址
	xp := (*ifaceWords)(unsafe.Pointer(&x))
	xp.typ = typ
	xp.data = data
	return
}

// Store sets the value of the Value to x.
// All calls to Store for a given Value must use values of the same concrete type.
// Store of an inconsistent type panics, as does Store(nil).
func (v *Value) Store(x interface{}) {
	if x == nil {
		panic("sync/atomic: store of nil value into Value")
    }
    // 强制类型转换, 可以认为所有的数据底层都是typ 和 data两部分构成, 所以这里可以转换成功
	vp := (*ifaceWords)(unsafe.Pointer(v))
	xp := (*ifaceWords)(unsafe.Pointer(&x))
	for {
        // 原子load 出现在存储的类型
        typ := LoadPointer(&vp.typ)
        
        // 如果是nil, 则说明还没有过存储, 则尝试cas nil 先存储真实数据的类型到 vp.typ
		if typ == nil {
			// Attempt to start first store.
			// Disable preemption so that other goroutines can use
			// active spin wait to wait for completion; and so that
			// GC does not see the fake type accidentally.
			// 编译时期会替换为runtime 包 proc.go 里面的 sync_runtime_procPin 这个方法主要是先找到当前G, 然后找到当前M, 将M里面locks++
			runtime_procPin()
			// 尝试第一次去store要存储的数据类型到 vp.typ 这里是原子的, 如果成功, 则表示此G来真正执行数据的store操作
			if !CompareAndSwapPointer(&vp.typ, nil, unsafe.Pointer(^uintptr(0))) {
				// m里面的locks--
				runtime_procUnpin()
				continue
			}
			// Complete first store.
			// 将数据的typ 和data 存储到vp 中
			StorePointer(&vp.data, xp.data)
			StorePointer(&vp.typ, xp.typ)
			runtime_procUnpin()
			return
		}

		// 这里说明程序刚执行到 78行, cas 那里, 并且刚结束, 所以需要continue 等待它执行完毕
		if uintptr(typ) == ^uintptr(0) {
			// First store in progress. Wait.
			// Since we disable preemption around the first store,
			// we can wait with active spinning.
			continue
		}
		// First store completed. Check type and overwrite data.
		// 如果发现存储的数据类型不一致, 则panic, 因为一个atomic.value 只允许存储同类型的数据
		if typ != xp.typ {
			panic("sync/atomic: store of inconsistently typed value into Value")
		}
		// 已经有数据了, 所以只需要重新存储数据, 不需要存储类型
		StorePointer(&vp.data, xp.data)
		return
	}
}

// Disable/enable preemption, implemented in runtime.
func runtime_procPin()
func runtime_procUnpin()



```