/*
 * Warp (C) 2019-2020 MinIO, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package bench

import (
	"context"
	"math"
	"os"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio/pkg/console"
	"github.com/minio/warp/pkg/generator"
)

type Benchmark interface {
	// Prepare for the benchmark run
	Prepare(ctx context.Context) error

	// Start will execute the main benchmark.
	// Operations should begin executing when the start channel is closed.
	Start(ctx context.Context, wait chan struct{}) (Operations, error)

	// Clean up after the benchmark run.
	Cleanup(ctx context.Context)

	// Common returns the common parameters.
	GetCommon() *Common
}

// Common contains common benchmark parameters.
type Common struct {
	Client func() (cl *minio.Client, done func())

	Concurrency int
	Source      func() generator.Source
	Prefix      string

	// Running in client mode.
	ClientMode bool
	// Clear prefix after benchmark
	Clear           bool
	PrepareProgress chan float64

	// Auto termination is set when this is > 0.
	AutoTermDur   time.Duration
	AutoTermScale float64

	// Default Put options.
	PutOpts minio.PutObjectOptions
}

const (
	// Split active ops into this many segments.
	autoTermSamples = 25

	// Number of segments that must be within limit.
	// The last segment will be the one considered 'current speed'.
	autoTermCheck = 7
)

func (c *Common) GetCommon() *Common {
	return c
}

// deleteAllInBucket will delete all content in a bucket.
// If no prefixes are specified everything in bucket is deleted.
func (c *Common) deleteAllInBucket(ctx context.Context, bucket string, prefixes ...string) {
	if len(prefixes) == 0 {
		prefixes = []string{""}
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-time.After(time.Minute):
			pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
		case <-finished:
			return
		}
	}()
	var wg sync.WaitGroup
	wg.Add(len(prefixes))
	for _, prefix := range prefixes {
		go func(prefix string) {
			defer wg.Done()

			doneCh := make(chan struct{})
			defer close(doneCh)
			cl, done := c.Client()
			defer done()
			remove := make(chan minio.ObjectInfo, 100)
			errCh := cl.RemoveObjects(ctx, bucket, remove, minio.RemoveObjectsOptions{})
			defer func() {
				// Signal we are done
				close(remove)
				// Wait for deletes to finish
				err := <-errCh
				if err.Err != nil {
					console.Error(err.Err)
				}
			}()

			objects := cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, WithVersions: true})
			for {
				select {
				case obj, ok := <-objects:
					if !ok {
						return
					}
					if obj.Err != nil {
						console.Error(obj.Err)
						continue
					}
				sendNext:
					for {
						select {
						case remove <- minio.ObjectInfo{
							Key:       obj.Key,
							VersionID: obj.VersionID,
						}:
							break sendNext
						case err := <-errCh:
							console.Error(err)
						}
					}
				case err := <-errCh:
					console.Error(err)
				}
			}
		}(prefix)
	}
	wg.Wait()

}

// prepareProgress updates preparation progess with the value 0->1.
func (c *Common) prepareProgress(progress float64) {
	if c.PrepareProgress == nil {
		return
	}
	progress = math.Max(0, math.Min(1, progress))
	select {
	case c.PrepareProgress <- progress:
	default:
	}
}
