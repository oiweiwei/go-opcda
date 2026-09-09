
MSIDLPATH ?= $(shell pwd)/idl:$(shell pwd)/idl/h

DOCKER_IMAGE ?= ghcr.io/oiweiwei/midl-gen-go

# verbose mode
ifeq ($(DEBUG),1)
DOCKER_RUNNER_FLAGS += --verbose
endif

DUMP_RUNNER ?= docker run --rm \
	-v $(shell pwd):/work \
	-u $(shell id -u):$(shell id -g) \
	$(DOCKER_IMAGE) dump \
	-I /work/idl \
	-I /work/idl/h

DOCKER_RUNNER ?= docker run --rm \
	-v $(shell pwd):/work \
	-v $(shell pwd)/../go-msrpc/idl:/work/go-msrpc/idl \
	-u $(shell id -u):$(shell id -g) \
	$(DOCKER_IMAGE) generate \
	--pkg "github.com/oiweiwei/go-opcda/opc/" \
	-I /work/go-msrpc/idl/ \
	-I /work/idl \
	-I /work/idl/h \
	--output /work/opc/

.PHONY: %.go
%.go:
	$(DOCKER_RUNNER) $(basename $@).idl


.PHONY: all
all:
	$(MAKE) \
		opccomn.go \
		opcda.go \
		opchda.go \
		opcsec.go \
		opcenum.go \
		sdk/unknwn.go \
		sdk/comcat.go \
		sdk/objidlbase.go

.PHONY: test
test:
	go test ./opc/...
