module ergo.services/actor/health/example

go 1.21

require (
	ergo.services/actor/health v0.0.0
	ergo.services/ergo v1.999.321-0.20260902074819-ba6b95f9188d
)

replace (
	ergo.services/actor/health => ../
	ergo.services/ergo => ../../../ergo
)
