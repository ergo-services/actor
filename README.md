[![Gitbook Documentation](https://img.shields.io/badge/GitBook-Documentation-f37f40?style=plastic&logo=gitbook&logoColor=white&style=flat)](https://docs.ergo.services/extra-library/actors)
[![Telegram Community](https://img.shields.io/badge/Telegram-ergo__services-229ed9?style=flat&logo=telegram&logoColor=white)](https://t.me/ergo_services)
[![Twitter](https://img.shields.io/badge/Twitter-ergo__services-00acee?style=flat&logo=twitter&logoColor=white)](https://twitter.com/ergo_services)
[![Reddit](https://img.shields.io/badge/Reddit-r/ergo__services-ff4500?style=plastic&logo=reddit&logoColor=white&style=flat)](https://reddit.com/r/ergo_services)

Extra library of actors for the Ergo Framework 3.0 (and above)

## saga
[![MIT license](https://img.shields.io/badge/license-MIT-brightgreen.svg)](https://opensource.org/licenses/MIT)

This actor implements Saga design pattern - a sequence of transactions that updates each service state and publishes the result (or cancels the transaction or triggers the next transaction step). It also provides a feature of interim results (can be used as transaction progress or as a part of pipeline processing), time deadline (to limit transaction lifespan), two-phase commit (to make distributed transaction atomic).

Doc: https://docs.ergo.services/extra-library/actors/saga

## sqrs 
[![MIT license](https://img.shields.io/badge/license-MIT-brightgreen.svg)](https://opensource.org/licenses/MIT)

Implements CQRS design pattern allowing to route incoming message flow based on the defined rules

Doc: https://docs.ergo.services/extra-library/actors/cqrs

## acme
[![MIT license](https://img.shields.io/badge/license-MIT-brightgreen.svg)](https://opensource.org/licenses/MIT)

Provides automatic access to certificates from Let's Encrypt and any other ACME-based CA

Doc: https://docs.ergo.services/extra-library/actors/acme
