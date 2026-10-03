export namespace main {
	
	export class AIModelCatalog {
	    current: string[];
	    available: string[];
	
	    static createFrom(source: any = {}) {
	        return new AIModelCatalog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current = source["current"];
	        this.available = source["available"];
	    }
	}
	export class CandidateConfirmRequest {
	    filename: string;
	    tmdb_id: number;
	    media_type: string;
	
	    static createFrom(source: any = {}) {
	        return new CandidateConfirmRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.tmdb_id = source["tmdb_id"];
	        this.media_type = source["media_type"];
	    }
	}
	export class CandidateSearchRequest {
	    filename: string;
	    title: string;
	    media_type: string;
	
	    static createFrom(source: any = {}) {
	        return new CandidateSearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.title = source["title"];
	        this.media_type = source["media_type"];
	    }
	}
	export class EnvDocument {
	    path: string;
	    content: string;
	    revision: string;
	
	    static createFrom(source: any = {}) {
	        return new EnvDocument(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	        this.revision = source["revision"];
	    }
	}
	export class FixRequest {
	    filename: string;
	    hint: string;
	    media_type?: string;
	
	    static createFrom(source: any = {}) {
	        return new FixRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.hint = source["hint"];
	        this.media_type = source["media_type"];
	    }
	}
	export class MetadataRepairResult {
	    movie: storage.Movie;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new MetadataRepairResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.movie = this.convertValues(source["movie"], storage.Movie);
	        this.warning = source["warning"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ScanFolders {
	    root: string;
	    folders: string[];
	    excluded: string[];
	
	    static createFrom(source: any = {}) {
	        return new ScanFolders(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.folders = source["folders"];
	        this.excluded = source["excluded"];
	    }
	}

}

export namespace storage {
	
	export class Movie {
	    id: number;
	    filename: string;
	    file_label?: string;
	    file_path?: string;
	    tmdb_id: number;
	    title_ua: string;
	    title_en: string;
	    year: string;
	    plot: string;
	    genres: string;
	    cast: string;
	    poster_url: string;
	    local_poster_path: string;
	    media_type: string;
	    recognition_source: string;
	    recognition_confidence: number;
	    verification_score: number;
	    needs_review: boolean;
	    review_reason?: string;
	    vote_average: number;
	    vote_count: number;
	
	    static createFrom(source: any = {}) {
	        return new Movie(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.filename = source["filename"];
	        this.file_label = source["file_label"];
	        this.file_path = source["file_path"];
	        this.tmdb_id = source["tmdb_id"];
	        this.title_ua = source["title_ua"];
	        this.title_en = source["title_en"];
	        this.year = source["year"];
	        this.plot = source["plot"];
	        this.genres = source["genres"];
	        this.cast = source["cast"];
	        this.poster_url = source["poster_url"];
	        this.local_poster_path = source["local_poster_path"];
	        this.media_type = source["media_type"];
	        this.recognition_source = source["recognition_source"];
	        this.recognition_confidence = source["recognition_confidence"];
	        this.verification_score = source["verification_score"];
	        this.needs_review = source["needs_review"];
	        this.review_reason = source["review_reason"];
	        this.vote_average = source["vote_average"];
	        this.vote_count = source["vote_count"];
	    }
	}

}

export namespace tmdb {
	
	export class CandidateDetails {
	    tmdb_id: number;
	    media_type: string;
	    title: string;
	    original_title: string;
	    release_date: string;
	    runtime: number;
	    genres: string;
	    overview: string;
	    poster_url: string;
	    vote_average: number;
	    vote_count: number;
	    short_film: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CandidateDetails(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tmdb_id = source["tmdb_id"];
	        this.media_type = source["media_type"];
	        this.title = source["title"];
	        this.original_title = source["original_title"];
	        this.release_date = source["release_date"];
	        this.runtime = source["runtime"];
	        this.genres = source["genres"];
	        this.overview = source["overview"];
	        this.poster_url = source["poster_url"];
	        this.vote_average = source["vote_average"];
	        this.vote_count = source["vote_count"];
	        this.short_film = source["short_film"];
	    }
	}
	export class TMDBCandidate {
	    tmdb_id: number;
	    title: string;
	    original_title: string;
	    year: number;
	    media_type: string;
	    popularity: number;
	
	    static createFrom(source: any = {}) {
	        return new TMDBCandidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tmdb_id = source["tmdb_id"];
	        this.title = source["title"];
	        this.original_title = source["original_title"];
	        this.year = source["year"];
	        this.media_type = source["media_type"];
	        this.popularity = source["popularity"];
	    }
	}

}

