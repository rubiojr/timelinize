/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

// Package smsbackuprestore implements a data source for the Android SMS Backup & Restore app by SyncTech:
// https://synctech.com.au/sms-backup-restore/
package smsbackuprestore

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "smsbackuprestore",
		Title:           "SMS Backup & Restore",
		Icon:            "smsbackuprestore.png",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

func (FileImporter) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	for _, filename := range filenames {
		result, err := recognizeFile(ctx, filename)
		if err != nil {
			return result, err
		}
		if result.Confidence == 0 {
			return result, nil
		}
	}
	return timeline.Recognition{Confidence: 1}, nil
}

func recognizeFile(ctx context.Context, filename string) (timeline.Recognition, error) {
	file, err := openFile(ctx, filename)
	if errors.Is(err, fs.ErrNotExist) {
		return timeline.Recognition{}, nil
	}
	if err != nil {
		return timeline.Recognition{}, fmt.Errorf("opening file: %v", err)
	}
	defer file.Close()

	// not a match if the file is a directory
	info, err := file.Stat()
	if err != nil {
		return timeline.Recognition{}, err
	}
	if info.IsDir() {
		return timeline.Recognition{}, nil
	}

	dec := xml.NewDecoder(file)

	for {
		// NOTE: I've seen JSON files successfully get a first token from the XML decoder
		tkn, err := dec.Token()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break // ignore short or empty files
		}
		if _, ok := err.(*xml.SyntaxError); ok {
			break // invalid XML file
		}
		if err != nil {
			// other errors indicate we're unsure whether we can recognize this
			return timeline.Recognition{}, fmt.Errorf("parsing XML token: %v", err)
		}

		if startElem, ok := tkn.(xml.StartElement); ok {
			if startElem.Name.Local == "smses" {
				// has the start of the expected XML structure!
				return timeline.Recognition{Confidence: 1}, nil
			} else {
				break
			}
		}
	}

	return timeline.Recognition{}, nil
}

// Options contains provider-specific options for using this data source.
type Options struct {
	// The phone number from which this export file originated.
	// SMS Backup & Restore does not provide any identifying
	// information of the recipient of these messages AT ALL,
	// so the user MUST supply their phone number.
	OwnerPhoneNumber string `json:"owner_phone_number"`

	// DefaultRegion is the region to assume for phone
	// numbers that do not have an explicit country
	// calling code. This value should be the ISO
	// 3166-1 alpha-2 standard region code.
	// Default: "US"
	DefaultRegion string `json:"default_region,omitempty"`
}

func (imp *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	dsOpt := *opt.DataSourceOptions.(*Options)

	if dsOpt.OwnerPhoneNumber == "" {
		return fmt.Errorf("owner phone number cannot be empty")
	}

	// standardize phone number, and ensure it is marked as identity
	standardizedPhoneNum, err := timeline.NormalizePhoneNumber(dsOpt.OwnerPhoneNumber, dsOpt.DefaultRegion)
	if err != nil {
		return fmt.Errorf("standardizing owner's phone number '%s': %v", dsOpt.OwnerPhoneNumber, err)
	}
	dsOpt.OwnerPhoneNumber = standardizedPhoneNum

	for _, filename := range filenames {
		if err := imp.importFile(ctx, filename, opt, dsOpt, itemChan); err != nil {
			return err
		}
	}

	return nil
}

func (imp *FileImporter) importFile(ctx context.Context, filename string, opt timeline.ListingOptions, dsOpt Options, itemChan chan<- *timeline.Graph) error {
	xmlFile, err := openFile(ctx, filename)
	if err != nil {
		return err
	}
	defer xmlFile.Close()

	// can't decode a directory
	info, err := xmlFile.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}

	// processing messages concurrently can be faster; but don't allow too many goroutines
	throttle := make(chan struct{}, 100)
	var wg sync.WaitGroup

	dec := xml.NewDecoder(xmlFile)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		tkn, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decoding next XML token: %v", err)
		}

		switch startElem := tkn.(type) {
		case xml.StartElement:
			switch startElem.Name.Local {
			case "sms":
				var sms SMS
				if err := dec.DecodeElement(&sms, &startElem); err != nil {
					return fmt.Errorf("decoding XML element as SMS: %v", err)
				}

				throttle <- struct{}{}
				wg.Add(1)
				go func() {
					defer func() {
						<-throttle
						wg.Done()
					}()
					if err := imp.processSMS(sms, opt, dsOpt, itemChan); err != nil {
						opt.Log.Error("processing SMS element", zap.Error(err))
					}
				}()
			case "mms":
				var mms MMS
				if err := dec.DecodeElement(&mms, &startElem); err != nil {
					return fmt.Errorf("decoding XML element as MMS: %v", err)
				}

				throttle <- struct{}{}
				wg.Add(1)
				go func() {
					defer func() {
						<-throttle
						wg.Done()
					}()
					if err := imp.processMMS(mms, opt, dsOpt, itemChan); err != nil {
						opt.Log.Error("processing MMS element", zap.Error(err))
					}
				}()
			}
		}
	}

	wg.Wait()

	return nil
}

func (imp *FileImporter) processSMS(sms SMS, opt timeline.ListingOptions, dsOpt Options, itemChan chan<- *timeline.Graph) error {
	if !sms.within(opt.Timeframe) {
		return nil
	}

	sender, receiver := sms.people(dsOpt)

	ig := &timeline.Graph{
		Item: &timeline.Item{
			Classification: timeline.ClassMessage,
			Timestamp:      time.UnixMilli(sms.Date),
			Owner:          sender,
			Content: timeline.ItemData{
				MediaType: "text/plain",
				Data:      timeline.StringData(strings.TrimSpace(sms.Body)),
			},
			Metadata: sms.metadata(),
		},
	}

	ig.ToEntity(timeline.RelSent, &receiver)

	itemChan <- ig

	return nil
}

func (imp *FileImporter) processMMS(mms MMS, opt timeline.ListingOptions, dsOpt Options, itemChan chan<- *timeline.Graph) error {
	if !mms.within(opt.Timeframe) {
		return nil
	}

	sender, recipients := mms.people(dsOpt)

	// the ordering of the parts is not guaranteed, and I've seen them
	// switched around on different exports; I think it makes sense to
	// prefer the part with text to be the "main" part as the root of
	// the graph, with media being attachments, or kind of secondary;
	// so move the text part to be first to have that guarantee.
	mms.Parts.putTextPartFirst()

	// TODO: what if the text part is empty? it results in a basically empty item,
	// with the media being attachments. Should the first non-empty part be used
	// as the main item instead?

	var ig *timeline.Graph
	for _, part := range mms.Parts.Part {
		if part.Seq < 0 {
			continue
		}

		// most MMS texts have useless rubbish filenames; ignore them since they waste space in the DB
		filename := part.Filename
		if _, ok := junkFilenames[filename]; ok {
			filename = ""
		}

		node := &timeline.Item{
			Classification: timeline.ClassMessage,
			//Timestamp:      time.Unix(0, mms.Date*int64(time.Millisecond)),
			Timestamp: time.UnixMilli(mms.Date),
			Owner:     sender,
			Content: timeline.ItemData{
				MediaType: part.ContentType,
				Filename:  part.Filename,
				Data:      part.data(),
			},
			Metadata: mms.metadata(),
		}

		if ig == nil {
			ig = &timeline.Graph{Item: node}
		} else {
			// TODO: this does not add a "sent" relation for the attachments,
			// we'd have to traverse up to the root of the graph (usually the text
			// node, if there is one) and then follow its "sent" edge to know
			// who the attachment was sent to... smaller DB I guess, is that OK though?
			ig.ToItem(timeline.RelAttachment, node)
		}
	}

	// some MMS are empty (or only have Seq=-1); no content means nil ItemGraph
	if ig == nil {
		return nil
	}

	// add relations to make sure other participants in a group text
	// are recorded; necessary if more than two participants
	for i := range recipients {
		ig.ToEntity(timeline.RelSent, &recipients[i])
	}

	itemChan <- ig

	return nil
}

// openFile opens the XML file at filename. However, as the Pro version
// of SMS Backup & Restore can compress them as .zip files, we also
// support that if the filename is a zip file. (The filename in the
// archive must be the same as the input filename without the .zip
// extension.)
func openFile(ctx context.Context, filename string) (fs.File, error) {
	fsys, err := archiver.FileSystem(ctx, filename)
	if err != nil {
		return nil, err
	}

	baseFilename := filepath.Base(filename)

	// the pro version of the app can compress the .xml file into a .zip file
	baseFilename = strings.TrimSuffix(baseFilename, ".zip")

	return fsys.Open(baseFilename)
}

// These filenames give us no information and waste space in the DB.
// And yes I have seen all of these myself.
var junkFilenames = map[string]struct{}{
	"null":            {},
	"0":               {},
	"text.000000.txt": {},
	"text.000001.txt": {},
	"text.000002.txt": {},
	"text000001.txt":  {},
	"text000002.txt":  {},
	"text000003.txt":  {},
	"text.txt":        {},
	"text_0.txt":      {},
	"text_1.txt":      {},
	"text_2.txt":      {},
	"image000000.jpg": {},
}

// From https://synctech.com.au/sms-backup-restore/fields-in-xml-backup-files/ (ca. May 2022)
const (
	unread = 0
	read   = 1

	smsTypeReceived = 1
	smsTypeSent     = 2
	smsTypeDraft    = 3
	smsTypeOutbox   = 4
	smsTypeFailed   = 5
	smsTypeQueued   = 6

	smsStatusNone     = -1
	smsStatusComplete = 0
	smsStatusPending  = 32
	smsStatusFailed   = 64

	mmsMsgBoxReceived = 1
	mmsMsgBoxSent     = 2
	mmsMsgBoxDraft    = 3
	mmsMsgBoxOutbox   = 4

	mmsAddrTypeBCC  = 129
	mmsAddrTypeCC   = 130
	mmsAddrTypeFrom = 137
	mmsAddrTypeTo   = 151
)
