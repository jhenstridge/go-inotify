# Copyright 2010 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

include ../../../Make.inc

TARG=os/inotify

GOFILES_linux=\
	inotify_linux.go\

GOFILES+=$(GOFILES_$(GOOS))

include ../../../Make.pkg
