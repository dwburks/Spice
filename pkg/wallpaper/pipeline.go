package wallpaper

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/time/rate"
	"runtime"
	"strings"
	"sync"

	"github.com/dixieflatline76/Spice/v2/pkg/provider"
	"github.com/dixieflatline76/Spice/v2/util/log"
)

// Pipeline manages a pool of workers to process image downloads.
type Pipeline struct {
	jobChan    chan DownloadJob
	dispatcher *Dispatcher
	resultChan chan ProcessResult
	cmdChan    chan StateCmd
	workerWg   sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	config     *Config
	store      *ImageStore
	processor  ProcessFunc
}

// DownloadJob represents a task to download and process an image.
type DownloadJob struct {
	Ctx      context.Context
	Image    provider.Image
	Provider provider.ImageProvider
	// ReuseOnly marks a re-admission from the retained index: the job must be served
	// from the master on disk and fails instead of touching the network.
	ReuseOnly bool
	// Done, if set, is called exactly once when the job's result has been admitted or
	// dropped (or the job was discarded before processing).
	Done func()
}

// finish signals job completion to its submitter, if it asked for it.
func (j DownloadJob) finish() {
	if j.Done != nil {
		j.Done()
	}
}

// ProcessResult represents the result of a processed image.
type ProcessResult struct {
	Image provider.Image
	Error error
	// JobID is the job's image ID; leased says the worker holds its job lease, which
	// stateManagerLoop releases after admitting (or dropping) the result.
	JobID  string
	leased bool
	done   func()
}

// ProcessFunc is the function signature for processing a job.
type ProcessFunc func(ctx context.Context, job DownloadJob) (provider.Image, error)

type CmdType int

const (
	CmdMarkSeen CmdType = iota
	CmdRemove
	CmdClear
)

type StateCmd struct {
	Type    CmdType
	Payload interface{}
}

// NewPipeline creates a new pipeline with the given configuration and store.
func NewPipeline(ctx context.Context, cfg *Config, store *ImageStore, processor ProcessFunc, apiLim func(provider.ImageProvider) *rate.Limiter, procLim func(provider.ImageProvider) *rate.Limiter) *Pipeline {
	ctx, cancel := context.WithCancel(ctx)
	p := &Pipeline{
		jobChan:    make(chan DownloadJob), // Unbuffered to strictly guarantee pacing execution
		resultChan: make(chan ProcessResult, 100),
		cmdChan:    make(chan StateCmd, 100),
		ctx:        ctx,
		cancel:     cancel,
		config:     cfg,
		store:      store,
		processor:  processor,
	}
	p.dispatcher = NewDispatcher(ctx, p.jobChan, apiLim, procLim, &p.workerWg)
	return p
}

// Start starts the worker pool.
func (p *Pipeline) Start(workerCount int) {
	log.Printf("Starting Pipeline with %d workers", workerCount)
	for i := 0; i < workerCount; i++ {
		p.workerWg.Add(1)
		go p.workerLoop(i)
	}

	// Start a collector goroutine to process results and add them to store
	// Start a collector goroutine to process results and add them to store
	go p.stateManagerLoop()
}

// Stop stops the pipeline and waits for workers to finish.
func (p *Pipeline) Stop() {
	log.Println("Stopping Pipeline...")
	p.cancel() // Signal cancellation
	p.workerWg.Wait()
	close(p.resultChan) // Close result channel after workers are done
	log.Println("Pipeline Stopped.")
}

// Submit submits a job to the pipeline perfectly paced by the Dispatcher.
// Returns false if pipeline is stopped or if the provided context is cancelled.
func (p *Pipeline) Submit(ctx context.Context, job DownloadJob) bool {
	// The Dispatcher natively handles provider cooldowns and drops the job
	// into p.jobChan ONLY when it is fully ready to be processed without sleeping.
	return p.dispatcher.Submit(job)
}

// workerLoop is the main loop for a worker goroutine.
func (p *Pipeline) workerLoop(id int) {
	defer p.workerWg.Done()
	log.Debugf("Worker %d started", id)

	for {
		select {
		case <-p.ctx.Done():
			log.Debugf("Worker %d stopping", id)
			return
		default:
			select {
			case <-p.ctx.Done():
				log.Debugf("Worker %d stopping", id)
				return
			case job := <-p.jobChan:
				// Ensure context exists (Testing safety net)
				if job.Ctx == nil {
					job.Ctx = p.ctx
				}

				// Own the image ID from processing through store admission, so cache
				// maintenance cannot delete files this job reuses or creates, and a
				// duplicate job for the same ID is rejected instead of racing it.
				id := job.Image.ID
				leased := false
				if p.store != nil && id != "" {
					if err := p.store.AcquireJobLease(job.Ctx, id); err != nil {
						p.resultChan <- ProcessResult{Error: fmt.Errorf("skipping %s: %w", id, err), JobID: id, done: job.Done}
						continue
					}
					leased = true
				}

				// Process the job using the job's context rather than the global pipeline context
				processedImg, err := p.processor(job.Ctx, job)
				p.resultChan <- ProcessResult{Image: processedImg, Error: err, JobID: id, leased: leased, done: job.Done}
			}
		}
	}
}

// StateManagerLoop is the single consumer that updates the store.
func (p *Pipeline) stateManagerLoop() {
	for {
		select {
		case res, ok := <-p.resultChan:
			if !ok {
				// Result channel closed, drain commands?
				// For now, if result chan closes, we assume pipeline stopping.
				return
			}
			p.admitResult(res)

		case cmd := <-p.cmdChan:
			switch cmd.Type {
			case CmdMarkSeen:
				p.store.MarkSeen(cmd.Payload.(string))
			case CmdRemove:
				id := cmd.Payload.(string)
				p.store.Remove(id)
				// log.Debugf("Pipeline: Removed image %s from store.", id)
			case CmdClear:
				p.store.Clear()
				log.Printf("Pipeline: Store cleared.")
			}
		case <-p.ctx.Done():
			p.drainResults()
			return
		}
		// Yield to allow Readers (UI) to acquire RLock
		runtime.Gosched()
	}
}

// admitResult lands one processed result in the store, then releases its job lease.
// A successful result that the store refuses (blocked meanwhile) has its files deleted
// once the lease is released; admitted downloads trigger a debounced disk-budget check.
func (p *Pipeline) admitResult(res ProcessResult) {
	defer func() {
		if res.done != nil {
			res.done()
		}
	}()
	if res.leased {
		defer p.store.ReleaseJobLease(res.JobID)
	}
	if res.Error != nil {
		p.logPipelineError(res.Error)
		if res.Image.ID != "" {
			p.store.Add(res.Image)
		}
		return
	}
	admitted := p.store.Add(res.Image)
	if !admitted {
		// Image already exists (re-processed via backlog healing).
		// Update so the fully-processed result with DerivativePaths lands.
		admitted = p.store.replace(res.Image)
	}
	if !admitted {
		p.store.MarkPendingDeleteIfUnowned(res.Image.ID)
		return
	}
	if p.config != nil {
		p.store.ScheduleBudgetCheck(p.config.GetCacheDiskBudgetBytes())
	}
}

// drainResults releases the leases of results still queued when the pipeline stops,
// without admitting them, until Stop closes the channel.
func (p *Pipeline) drainResults() {
	for res := range p.resultChan {
		if res.leased {
			p.store.ReleaseJobLease(res.JobID)
		}
		if res.done != nil {
			res.done()
		}
	}
}

// SendCommand sends a state mutation command to the pipeline manager.
func (p *Pipeline) SendCommand(cmd StateCmd) {
	select {
	case p.cmdChan <- cmd:
	case <-p.ctx.Done():
	}
}

// logPipelineError categorizes and logs errors, ignoring expected ones.
func (p *Pipeline) logPipelineError(err error) {
	errMsg := err.Error()
	if strings.Contains(errMsg, "avoid set") {
		log.Debugf("Pipeline: %v", err)
	} else if strings.Contains(errMsg, "smart fit") || strings.Contains(errMsg, "aspect ratio") || strings.Contains(errMsg, "image resolution too low") {
		log.Debugf("Pipeline: %v", err)
	} else if strings.Contains(errMsg, "status 429") || strings.Contains(errMsg, "enrichment") {
		log.Debugf("Pipeline: %v", err)
	} else if strings.Contains(errMsg, "incompatible") {
		log.Debugf("Pipeline: %v", err)
	} else if errors.Is(err, ErrDownloadTooLarge) || errors.Is(err, errJobInFlight) || errors.Is(err, context.Canceled) || strings.Contains(errMsg, "no longer on disk") {
		log.Debugf("Pipeline: %v", err)
	} else {
		log.Printf("Pipeline Error: %v", err)
	}
}
