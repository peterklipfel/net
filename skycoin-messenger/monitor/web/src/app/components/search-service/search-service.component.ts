import { Component, OnInit, ViewEncapsulation } from '@angular/core';
import { ApiService } from '../../service';
import { Observable } from 'rxjs/Observable';
import { BehaviorSubject } from 'rxjs/BehaviorSubject';
import { Subject } from 'rxjs/Subject';
import { Subscription } from 'rxjs/Subscription';
import { MatDialogRef } from '@angular/material';

import 'rxjs/add/operator/take';
import 'rxjs/add/observable/interval';

@Component({
  selector: 'app-search-service',
  templateUrl: 'search-service.component.html',
  styleUrls: ['./search-service.component.scss'],
  encapsulation: ViewEncapsulation.None
})

export class SearchServiceComponent implements OnInit {
  searchStr = '';
  nodeAddr = '';
  seqs = [];
  timeOut = 1;
  resultTask: Subscription = null;
  totalResults: Array<Search> = [];
  results: Array<Search> = [];
  status = 0;
  private result: Subject<Array<Search>> = new BehaviorSubject<Array<Search>>([]);
  constructor(private api: ApiService, private dialogRef: MatDialogRef<SearchServiceComponent>) { }
  ngOnInit() {
    this.handle();
    this.refresh();
  }
  connectSocket(nodeKey: string, appKey: string) {
    if (!nodeKey || !appKey) {
      return;
    }
    const data = new FormData();
    const jsonStr = {
      label: '',
      nodeKey: nodeKey,
      appKey: appKey,
      count: 1
    };
    data.append('client', 'socket');
    data.append('data', JSON.stringify(jsonStr));
    this.api.saveClientConnection(data).subscribe(res => {
      data.delete('data');
      data.delete('client');
    });
    data.append('toNode', nodeKey);
    data.append('toApp', appKey);
    this.api.connectSocketClicent(this.nodeAddr, data).subscribe(result => {
      console.log('conect socket client');
      this.dialogRef.close(result);
    });
  }
  refresh(ev?: Event) {
    if (ev) {
      ev.stopImmediatePropagation();
      ev.stopPropagation();
      ev.preventDefault();
    }
    this.status = 0;
    this.search();
    this.getResult();
    setTimeout(() => {
      this.status = 1;
    }, (this.timeOut + 2) * 1000);
  }
  search() {
    const data = new FormData();
    data.append('key', this.searchStr);
    Observable.interval(1000).take(this.timeOut).subscribe(() => {
      this.api.searchServices(this.nodeAddr, data).subscribe(seq => {
        this.seqs = this.seqs.concat(seq);
      });
    });
  }

  getResult() {
    Observable.interval(1000).take(this.timeOut + 2).subscribe(() => {
      this.api.getServicesResult(this.nodeAddr).subscribe(result => {
        console.log('result:', result);
        this.result.next(result);
      });
    });
  }
  handle() {
    this.result.subscribe((results: Array<Search>) => {
      const tmp = this.filterSeq(results);
      this.unique(tmp);
      this.sortByKey();
      console.log('total:', this.totalResults);
      this.results = this.totalResults;
    });
  }
  sortByKey() {
    for (let index = 0; index < this.totalResults.length; index++) {
      Object.keys(this.totalResults[index].result).sort(
        function (a, b) {
          return a.localeCompare(b);
        });
    }
  }
  trackByKey(index, app) {
    return app ? app.key : undefined;
  }
  sort() {
    this.totalResults.sort((v1, v2) => {
      if (v1.seq < v2.seq) {
        return -1;
      }
      if (v1.seq > v2.seq) {
        return 1;
      }
      return 0;
    });
  }
  filterSeq(results: Array<Search>) {
    const tmpResults: Array<Search> = [];
    if (!results) {
      return;
    }
    results.forEach(result => {
      if (this.seqs.indexOf(result.seq) > - 1) {
        tmpResults.push(result);
      }
    });
    return tmpResults;
  }
  unique(results: Array<Search>) {
    if (!results) {
      return;
    }
    this.totalResults = results;
    return;
  }
}

export interface Search {
  result?: Map<string, Array<string>>;
  seq?: number;
}

export interface SearchResult {
  node_key?: string;
  apps?: Array<string>;
}
