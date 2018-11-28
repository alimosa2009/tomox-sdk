package swap

import (
	"math/big"
	"os"
	"os/signal"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/labstack/gommon/log"
	"github.com/tomochain/backend-matching-engine/interfaces"
	"github.com/tomochain/backend-matching-engine/swap/config"
	"github.com/tomochain/backend-matching-engine/swap/errors"
	"github.com/tomochain/backend-matching-engine/swap/ethereum"
	"github.com/tomochain/backend-matching-engine/swap/queue"
	"github.com/tomochain/backend-matching-engine/swap/tomochain"
	"github.com/tomochain/backend-matching-engine/utils"
)

// swap is engine
var logger = utils.EngineLogger

// JS SDK use to communicate.
const ProtocolVersion int = 2

type Engine struct {
	Config                       *config.Config                 `inject:""`
	ethereumListener             *ethereum.Listener             `inject:""`
	ethereumAddressGenerator     *ethereum.AddressGenerator     `inject:""`
	tomochainAccountConfigurator *tomochain.AccountConfigurator `inject:""`
	transactionsQueue            queue.Queue                    `inject:""`

	minimumValueEth string
	signerPublicKey string

	minimumValueSat int64
	minimumValueWei *big.Int
}

func NewEngine(cfg *config.Config) *Engine {
	engine := &Engine{
		signerPublicKey: cfg.SignerPublicKey(),
	}
	if cfg.Ethereum != nil {
		ethereumListener := &ethereum.Listener{}
		ethereumClient, err := ethclient.Dial("http://" + cfg.Ethereum.RpcServer)
		if err != nil {
			logger.Error("Error connecting to geth")
			os.Exit(-1)
		}

		// config ethereum listener
		ethereumListener.Enabled = true
		ethereumListener.NetworkID = cfg.Ethereum.NetworkID
		ethereumListener.Client = ethereumClient

		engine.minimumValueEth = cfg.Ethereum.MinimumValueEth

		ethereumAddressGenerator, err := ethereum.NewAddressGenerator(cfg.Ethereum.MasterPublicKey)
		if err != nil {
			log.Error(err)
			os.Exit(-1)
		}

		engine.ethereumAddressGenerator = ethereumAddressGenerator
		engine.ethereumListener = ethereumListener
	}

	if cfg.Tomochain != nil {

		tomochainAccountConfigurator := &tomochain.AccountConfigurator{
			IssuerPublicKey:       cfg.Tomochain.IssuerPublicKey,
			DistributionPublicKey: cfg.Tomochain.DistributionPublicKey,
			SignerPrivateKey:      cfg.Tomochain.SignerPrivateKey,
			TokenAssetCode:        cfg.Tomochain.TokenAssetCode,
			StartingBalance:       cfg.Tomochain.StartingBalance,
			LockUnixTimestamp:     cfg.Tomochain.LockUnixTimestamp,
		}

		if cfg.Tomochain.StartingBalance == "" {
			tomochainAccountConfigurator.StartingBalance = "2.1"
		}

		if cfg.Ethereum != nil {
			tomochainAccountConfigurator.TokenPriceETH = cfg.Ethereum.TokenPrice
		}

		engine.tomochainAccountConfigurator = tomochainAccountConfigurator
	}

	engine.Config = cfg
	return engine
}

func (engine *Engine) SetDelegate(handler interfaces.SwapEngineHandler) {
	// delegate some handlers
	engine.ethereumListener.TransactionHandler = handler.OnNewEthereumTransaction
	engine.tomochainAccountConfigurator.OnAccountCreated = handler.OnTomochainAccountCreated
	engine.tomochainAccountConfigurator.OnExchanged = handler.OnExchanged
	engine.tomochainAccountConfigurator.OnExchangedTimelocked = handler.OnExchangedTimelocked
}

func (engine *Engine) Start() error {

	var err error
	engine.minimumValueWei, err = ethereum.EthToWei(engine.minimumValueEth)
	if err != nil {
		return errors.Wrap(err, "Invalid minimum accepted Ethereum transaction value")
	}

	if engine.minimumValueWei.Cmp(new(big.Int)) == 0 {
		return errors.New("Minimum accepted Ethereum transaction value must be larger than 0")
	}

	err = engine.ethereumListener.Start(engine.Config.Ethereum.RpcServer)
	if err != nil {
		return errors.Wrap(err, "Error starting EthereumListener")
	}

	err = engine.tomochainAccountConfigurator.Start()
	if err != nil {
		return errors.Wrap(err, "Error starting TomochainAccountConfigurator")
	}

	// client will update swarm feed association so that we do not have to build broadcast engine

	signalInterrupt := make(chan os.Signal, 1)
	signal.Notify(signalInterrupt, os.Interrupt)

	go engine.poolTransactionsQueue()

	<-signalInterrupt
	engine.shutdown()

	return nil
}

func (engine *Engine) TransactionsQueue() queue.Queue {
	return engine.transactionsQueue
}

// public method to access private properties, this avoids setting props directly cause mistmatch from config
func (engine *Engine) EthereumAddressGenerator() *ethereum.AddressGenerator {
	return engine.ethereumAddressGenerator
}

func (engine *Engine) TomochainAccountConfigurator() *tomochain.AccountConfigurator {
	return engine.tomochainAccountConfigurator
}

func (engine *Engine) MinimumValueEth() string {
	return engine.minimumValueEth
}

func (engine *Engine) SignerPublicKey() string {
	return engine.signerPublicKey
}

func (engine *Engine) MinimumValueSat() int64 {
	return engine.minimumValueSat
}

func (engine *Engine) MinimumValueWei() *big.Int {
	return engine.minimumValueWei
}

// poolTransactionsQueue pools transactions queue which contains only processed and
// validated transactions and sends it to TomochainAccountConfigurator for account configuration.
func (engine *Engine) poolTransactionsQueue() {
	logger.Infof("Started pooling transactions queue")

	for {
		transaction, err := engine.transactionsQueue.QueuePool()
		if err != nil {
			logger.Infof("Error pooling transactions queue")
			time.Sleep(time.Second)
			continue
		}

		if transaction == nil {
			time.Sleep(time.Second)
			continue
		}

		logger.Infof("Received transaction from transactions queue: %v", transaction)
		go engine.tomochainAccountConfigurator.ConfigureAccount(
			transaction.TomochainPublicKey,
			string(transaction.AssetCode),
			transaction.Amount,
		)
	}
}

func (e *Engine) shutdown() {
	// do something
}