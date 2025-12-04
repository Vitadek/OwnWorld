func runGameLoop() {
	InfoLog.Println("Starting Galaxy Engine (Time Lord Mode)...")
	
	for {
		// 1. Calculate Offset based on Election/Rank
		offset := CalculateOffset()
		
		// 2. Determine Target Time (Global Grid 5s)
		now := time.Now().UnixMilli()
		// Align to the next 5-second window + offset
		target := ((now / 5000) * 5000) + 5000 + offset.Milliseconds()
		
		// 3. Sleep until Target
		sleep := time.Until(time.UnixMilli(target))
		if sleep > 0 { time.Sleep(sleep) }

		// 4. Execute Tick
		tickWorld()
	}
}
