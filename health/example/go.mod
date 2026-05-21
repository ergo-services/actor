module ergo.services/actor/health/example

go 1.21

require (
	ergo.services/actor/health v0.0.0
	ergo.services/ergo v1.999.321-0.20260521070644-eb451aa1e067
)

replace (
	ergo.services/actor/health => ../
	ergo.services/ergo => ../../../ergo
)
