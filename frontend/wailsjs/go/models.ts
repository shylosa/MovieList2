export namespace storage {
	
	export class Movie {
	    id: number;
	    filename: string;
	    tmdb_id: number;
	    title_ua: string;
	    title_en: string;
	    year: string;
	    plot: string;
	    genres: string;
	    cast: string;
	    poster_url: string;
	    local_poster_path: string;
	
	    static createFrom(source: any = {}) {
	        return new Movie(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.filename = source["filename"];
	        this.tmdb_id = source["tmdb_id"];
	        this.title_ua = source["title_ua"];
	        this.title_en = source["title_en"];
	        this.year = source["year"];
	        this.plot = source["plot"];
	        this.genres = source["genres"];
	        this.cast = source["cast"];
	        this.poster_url = source["poster_url"];
	        this.local_poster_path = source["local_poster_path"];
	    }
	}

}

